package cfworkers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	testZone     = "0123456789abcdef0123456789abcdef"
	testHostID   = "0d89c70d-ad9f-4843-b99f-6cc0252067e9"
	pendingReply = `{"id":"0d89c70d-ad9f-4843-b99f-6cc0252067e9","hostname":"www.example.com","status":"pending",
		"verification_errors":["custom hostname does not CNAME to this zone."],
		"ownership_verification":{"type":"txt","name":"_cf-custom-hostname.www.example.com","value":"5cc07c04"},
		"ownership_verification_http":{"http_url":"http://www.example.com/.well-known/cf-custom-hostname-challenge/x","http_body":"5cc07c04"},
		"ssl":{"status":"pending_validation","method":"txt","type":"dv",
			"validation_records":[{"txt_name":"_acme-challenge.www.example.com","txt_value":"810b7d5f","status":"pending"}],
			"validation_errors":[{"message":"SERVFAIL looking up CAA for www.example.com"}]}}`
)

type zoneFake struct {
	mu       sync.Mutex
	requests []recorded
}

func newZoneFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) (*zoneFake, *ZoneClient) {
	t.Helper()
	fake := &zoneFake{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		fake.mu.Unlock()
		handle(w, r, body)
	}))
	t.Cleanup(server.Close)
	client, err := NewZoneClient(ZoneConfig{BaseURL: server.URL, APIToken: testToken, ZoneID: testZone})
	if err != nil {
		t.Fatalf("NewZoneClient: %v", err)
	}
	return fake, client
}

func writeRaw(w http.ResponseWriter, status int, result string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"success":` + map[bool]string{true: "true", false: "false"}[status < 300] + `,"errors":[],"result":` + result + `}`))
}

func TestNewZoneClientRefusesMissingSettings(t *testing.T) {
	for _, cfg := range []ZoneConfig{{APIToken: "t"}, {ZoneID: testZone}, {APIToken: "t", ZoneID: "../zones"}} {
		if _, err := NewZoneClient(cfg); err == nil {
			t.Errorf("NewZoneClient(%+v) accepted it", cfg)
		}
	}
}

func TestCreateCustomHostnameAsksForADVCertificateValidatedOverHTTP(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeRaw(w, http.StatusOK, pendingReply)
	})
	host, err := client.CreateCustomHostname(context.Background(), "www.example.com", ValidationHTTP)
	if err != nil {
		t.Fatalf("CreateCustomHostname: %v", err)
	}
	got := fake.requests[0]
	if got.method != http.MethodPost || got.path != "/client/v4/zones/"+testZone+"/custom_hostnames" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+testToken || got.contentType != "application/json" {
		t.Errorf("auth %q type %q", got.auth, got.contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	ssl, _ := body["ssl"].(map[string]any)
	if body["hostname"] != "www.example.com" || ssl["method"] != "http" || ssl["type"] != "dv" || len(body) != 2 {
		t.Errorf("body = %s", got.body)
	}
	if host.ID != testHostID || host.Status != "pending" || host.SSL.Status != "pending_validation" {
		t.Errorf("host = %+v", host)
	}
	if host.OwnershipVerification.Name != "_cf-custom-hostname.www.example.com" || host.OwnershipVerification.Value != "5cc07c04" {
		t.Errorf("ownership = %+v", host.OwnershipVerification)
	}
	if len(host.SSL.ValidationRecords) != 1 || host.SSL.ValidationRecords[0].TXTName != "_acme-challenge.www.example.com" {
		t.Errorf("validation records = %+v", host.SSL.ValidationRecords)
	}
	if len(host.VerificationErrors) != 1 || len(host.SSL.ValidationErrors) != 1 {
		t.Errorf("errors = %v %v", host.VerificationErrors, host.SSL.ValidationErrors)
	}
}

func TestCreateCustomHostnameRefusesAnUnknownValidationMethod(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeRaw(w, 200, pendingReply) })
	if _, err := client.CreateCustomHostname(context.Background(), "www.example.com", "email"); err == nil {
		t.Fatal("email validation was sent")
	}
}

func TestCreateCustomHostnameReportsCloudflaresRefusal(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1406,"message":"Duplicate custom hostname found."}]}`))
	})
	_, err := client.CreateCustomHostname(context.Background(), "www.example.com", ValidationHTTP)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !IsDuplicateCustomHostname(err) {
		t.Fatalf("err = %v, want the duplicate refusal", err)
	}
}

func TestGetCustomHostnameReadsOneByID(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeRaw(w, 200, pendingReply) })
	if _, err := client.GetCustomHostname(context.Background(), testHostID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := fake.requests[0]; got.method != http.MethodGet || got.path != "/client/v4/zones/"+testZone+"/custom_hostnames/"+testHostID {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if _, err := client.GetCustomHostname(context.Background(), "../dns_records"); err == nil {
		t.Fatal("an id that is not Cloudflare's shape reached the path")
	}
}

// A no-change PATCH asks Cloudflare to run domain control validation again now.
func TestRevalidateSendsTheSameSSLSettingsAgain(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeRaw(w, 200, pendingReply) })
	if _, err := client.RevalidateCustomHostname(context.Background(), testHostID, ValidationTXT); err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	got := fake.requests[0]
	if got.method != http.MethodPatch || got.path != "/client/v4/zones/"+testZone+"/custom_hostnames/"+testHostID {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if string(got.body) != `{"ssl":{"method":"txt","type":"dv"}}` {
		t.Errorf("body = %s", got.body)
	}
}

func TestFindCustomHostnameMatchesOnlyTheExactName(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeRaw(w, 200, `[{"id":"11111111-1111-1111-1111-111111111111","hostname":"shop.www.example.com"},`+pendingReply+`]`)
	})
	host, err := client.FindCustomHostname(context.Background(), "www.example.com")
	if err != nil || host == nil || host.ID != testHostID {
		t.Fatalf("host = %+v err = %v", host, err)
	}
	if got := fake.requests[0]; got.method != http.MethodGet || got.query != "hostname.exact=www.example.com" {
		t.Fatalf("request = %s ?%s", got.method, got.query)
	}
}

func TestFindCustomHostnameAnswersNilWhenNoneMatches(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeRaw(w, 200, `[{"id":"11111111-1111-1111-1111-111111111111","hostname":"other.example.com"}]`)
	})
	host, err := client.FindCustomHostname(context.Background(), "www.example.com")
	if err != nil || host != nil {
		t.Fatalf("host = %+v err = %v", host, err)
	}
}

func TestDeleteCustomHostnameTreatsAMissingOneAsDeleted(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1436,"message":"not found"}]}`))
	})
	if err := client.DeleteCustomHostname(context.Background(), testHostID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := fake.requests[0]; got.method != http.MethodDelete || got.path != "/client/v4/zones/"+testZone+"/custom_hostnames/"+testHostID {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
}

func TestDeleteCustomHostnameReportsOtherFailures(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if err := client.DeleteCustomHostname(context.Background(), testHostID); err == nil {
		t.Fatal("want the failure")
	}
}

func TestFallbackOriginIsReadAndSet(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeRaw(w, 200, `{"origin":"cname.excalibase.app","status":"pending_deployment","errors":["DNS records are not setup correctly"]}`)
	})
	origin, err := client.FallbackOrigin(context.Background())
	if err != nil || origin.Origin != "cname.excalibase.app" || origin.Status != "pending_deployment" || len(origin.Errors) != 1 {
		t.Fatalf("origin = %+v err = %v", origin, err)
	}
	if _, err := client.SetFallbackOrigin(context.Background(), "cname.excalibase.app"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	wantPath := "/client/v4/zones/" + testZone + "/custom_hostnames/fallback_origin"
	if got := fake.requests[0]; got.method != http.MethodGet || got.path != wantPath {
		t.Fatalf("read = %s %s", got.method, got.path)
	}
	if got := fake.requests[1]; got.method != http.MethodPut || got.path != wantPath || string(got.body) != `{"origin":"cname.excalibase.app"}` {
		t.Fatalf("write = %s %s %s", got.method, got.path, got.body)
	}
}

// A zone with no fallback origin yet answers 404; that reads as none set.
func TestFallbackOriginNotSetYetIsEmpty(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1551,"message":"No fallback origin"}]}`))
	})
	origin, err := client.FallbackOrigin(context.Background())
	if err != nil || origin.Origin != "" {
		t.Fatalf("origin = %+v err = %v", origin, err)
	}
}

func TestDNSRecordsAreFoundByExactNameAndCreatedProxied(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet {
			writeRaw(w, 200, `[{"id":"r1","type":"AAAA","name":"cname.excalibase.app","content":"100::","proxied":true},{"id":"r2","type":"AAAA","name":"x.cname.excalibase.app","content":"100::","proxied":true}]`)
			return
		}
		writeRaw(w, 200, `{"id":"r3","type":"AAAA","name":"cname.excalibase.app","content":"100::","proxied":true}`)
	})
	records, err := client.DNSRecords(context.Background(), "cname.excalibase.app")
	if err != nil || len(records) != 1 || records[0].ID != "r1" || !records[0].Proxied {
		t.Fatalf("records = %+v err = %v", records, err)
	}
	if _, err := client.CreateDNSRecord(context.Background(), DNSRecord{Type: "AAAA", Name: "cname.excalibase.app", Content: "100::", Proxied: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := fake.requests[0]; got.path != "/client/v4/zones/"+testZone+"/dns_records" || got.query != "name.exact=cname.excalibase.app" {
		t.Fatalf("list = %s ?%s", got.path, got.query)
	}
	got := fake.requests[1]
	if got.method != http.MethodPost || string(got.body) != `{"type":"AAAA","name":"cname.excalibase.app","content":"100::","proxied":true,"ttl":1}` {
		t.Fatalf("create = %s %s", got.method, got.body)
	}
}

func TestWorkerRoutesAreListedAndCreated(t *testing.T) {
	fake, client := newZoneFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet {
			writeRaw(w, 200, `[{"id":"a1","pattern":"*.excalibase.app/*","script":"excalibase-sites-router"}]`)
			return
		}
		writeRaw(w, 200, `{"id":"a2","pattern":"*/*","script":"excalibase-sites-router"}`)
	})
	routes, err := client.WorkerRoutes(context.Background())
	if err != nil || len(routes) != 1 || routes[0].Pattern != "*.excalibase.app/*" {
		t.Fatalf("routes = %+v err = %v", routes, err)
	}
	if _, err := client.CreateWorkerRoute(context.Background(), "*/*", "excalibase-sites-router"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := fake.requests[1]
	if got.method != http.MethodPost || got.path != "/client/v4/zones/"+testZone+"/workers/routes" || string(got.body) != `{"pattern":"*/*","script":"excalibase-sites-router"}` {
		t.Fatalf("create = %s %s %s", got.method, got.path, got.body)
	}
}

func TestZoneCallsNeverCarryTheTokenInAnError(t *testing.T) {
	_, client := newZoneFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	})
	_, err := client.CreateCustomHostname(context.Background(), "www.example.com", ValidationHTTP)
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), testZone) {
		t.Fatalf("err = %v", err)
	}
}
