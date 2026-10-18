package s3keys

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

const project = "proj-1"

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

type harness struct {
	svc      *Service
	store    *fakeStore
	secrets  *fakeSecrets
	standing *fakeStanding
	clock    time.Time
}

func newHarness() *harness {
	h := &harness{store: newFakeStore(), secrets: newFakeSecrets(), standing: &fakeStanding{lost: map[string]bool{}}, clock: now}
	h.svc = NewService(h.store, h.secrets, h.standing, fakeBuckets{project + "/photos": true, project + "/backups": true})
	h.svc.now = func() time.Time { return h.clock }
	return h
}

func readWrite() CreateRequest {
	return CreateRequest{ProjectID: project, Name: "rclone on the NAS", Actor: "user-dev",
		Permissions: Permissions{Read: true, Write: true}}
}

func wantInputError(t *testing.T, err error) {
	t.Helper()
	var input *InputError
	if !errors.As(err, &input) {
		t.Fatalf("err = %v, want an InputError", err)
	}
}

func TestCreate_ReturnsTheSecretOnceAndKeepsItOnlyInTheSecretStore(t *testing.T) {
	h := newHarness()
	secret, key, err := h.svc.Create(context.Background(), readWrite())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^EXS3[A-Z2-7]{16}$`).MatchString(key.AccessKeyID) {
		t.Fatalf("access key id = %q", key.AccessKeyID)
	}
	if len(secret) != 40 || strings.ContainsAny(secret, "+/=") {
		t.Fatalf("secret = %q, want 40 url-safe characters", secret)
	}
	if h.secrets.secrets[project+"/"+key.AccessKeyID] != secret {
		t.Fatal("the secret is in the secret store under the project")
	}
	listed, err := h.svc.List(context.Background(), project)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed = %v, %v", listed, err)
	}
	if listed[0].CreatedBy != "user-dev" || !listed[0].CreatedAt.Equal(now) || listed[0].ExpiresAt != nil {
		t.Fatalf("listed = %+v", listed[0])
	}
}

func TestCreate_TwoKeysNeverShareAnIDOrSecret(t *testing.T) {
	h := newHarness()
	secretA, keyA, _ := h.svc.Create(context.Background(), readWrite())
	secretB, keyB, _ := h.svc.Create(context.Background(), readWrite())
	if keyA.AccessKeyID == keyB.AccessKeyID || secretA == secretB || keyA.ID == keyB.ID {
		t.Fatal("keys must be unique")
	}
}

func TestCreate_Validation(t *testing.T) {
	cases := map[string]func(r *CreateRequest){
		"no name":            func(r *CreateRequest) { r.Name = "  " },
		"name too long":      func(r *CreateRequest) { r.Name = strings.Repeat("n", 65) },
		"control characters": func(r *CreateRequest) { r.Name = "a\x00b" },
		"no permission":      func(r *CreateRequest) { r.Permissions = Permissions{} },
		"unknown bucket":     func(r *CreateRequest) { r.Buckets = []string{"photos", "nope"} },
		"too many buckets":   func(r *CreateRequest) { r.Buckets = make([]string, MaxBucketsPerKey+1) },
		"lifetime under 1d":  func(r *CreateRequest) { r.Lifetime = time.Hour },
		"lifetime over 365d": func(r *CreateRequest) { r.Lifetime = 366 * 24 * time.Hour },
		"negative lifetime":  func(r *CreateRequest) { r.Lifetime = -24 * time.Hour },
		"expired credential": func(r *CreateRequest) { past := now.Add(-time.Minute); r.NotAfter = &past },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			request := readWrite()
			change(&request)
			_, _, err := h.svc.Create(context.Background(), request)
			wantInputError(t, err)
			if len(h.store.keys) != 0 || len(h.secrets.secrets) != 0 {
				t.Fatal("a refused key leaves nothing behind")
			}
		})
	}
}

func TestCreate_BucketsAreDedupedAndSorted(t *testing.T) {
	h := newHarness()
	request := readWrite()
	request.Buckets = []string{"photos", "backups", "photos"}
	_, key, err := h.svc.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(key.Buckets, ",") != "backups,photos" {
		t.Fatalf("buckets = %v", key.Buckets)
	}
}

func TestCreate_ExpiryNeverOutlivesTheCallersCredential(t *testing.T) {
	h := newHarness()
	request := readWrite()
	request.Lifetime = 30 * 24 * time.Hour
	patExpiry := now.Add(7 * 24 * time.Hour)
	request.NotAfter = &patExpiry
	_, key, err := h.svc.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if key.ExpiresAt == nil || !key.ExpiresAt.Equal(patExpiry) {
		t.Fatalf("expires = %v, want the PAT's %v", key.ExpiresAt, patExpiry)
	}
	request.Lifetime = 0
	_, key, _ = h.svc.Create(context.Background(), request)
	if key.ExpiresAt == nil || !key.ExpiresAt.Equal(patExpiry) {
		t.Fatalf("a key with no lifetime made by a PAT still expires with it: %v", key.ExpiresAt)
	}
}

func TestCreate_LimitPerProject(t *testing.T) {
	h := newHarness()
	for i := 0; i < MaxKeysPerProject; i++ {
		if _, _, err := h.svc.Create(context.Background(), readWrite()); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := h.svc.Create(context.Background(), readWrite())
	if !errors.Is(err, ErrLimitReached) {
		t.Fatalf("err = %v", err)
	}
	if len(h.secrets.secrets) != MaxKeysPerProject {
		t.Fatalf("the refused key's secret must be removed again; %d held", len(h.secrets.secrets))
	}
}

func TestCreate_SecretStoreFailureCreatesNothing(t *testing.T) {
	h := newHarness()
	h.secrets.err = errors.New("vault is sealed")
	if _, _, err := h.svc.Create(context.Background(), readWrite()); err == nil {
		t.Fatal("want an error")
	}
	if len(h.store.keys) != 0 {
		t.Fatal("no row without a secret")
	}
}

func TestResolve(t *testing.T) {
	h := newHarness()
	secret, key, _ := h.svc.Create(context.Background(), readWrite())
	resolved, gotSecret, err := h.svc.Resolve(context.Background(), key.AccessKeyID)
	if err != nil || gotSecret != secret || resolved.ID != key.ID {
		t.Fatalf("resolved %+v %q %v", resolved, gotSecret, err)
	}
	if !h.store.touched[key.ID].Equal(now) {
		t.Fatal("a use is recorded")
	}
	if _, _, err := h.svc.Resolve(context.Background(), "EXS3AAAAAAAAAAAAAAAA"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
}

func TestResolve_CachesTheSecretButChecksTheRowEveryTime(t *testing.T) {
	h := newHarness()
	_, key, _ := h.svc.Create(context.Background(), readWrite())
	for i := 0; i < 3; i++ {
		if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); err != nil {
			t.Fatal(err)
		}
	}
	if h.secrets.gets != 1 {
		t.Fatalf("secret read %d times, want once", h.secrets.gets)
	}
	if _, err := h.svc.Revoke(context.Background(), project, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("a revoked key is refused at once, cached secret or not: %v", err)
	}
}

func TestResolve_ExpiredKeyIsUnknown(t *testing.T) {
	h := newHarness()
	request := readWrite()
	request.Lifetime = 24 * time.Hour
	_, key, _ := h.svc.Create(context.Background(), request)
	h.clock = now.Add(24 * time.Hour)
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve_KeyIsRevokedWhenItsCreatorLosesStanding(t *testing.T) {
	h := newHarness()
	_, key, _ := h.svc.Create(context.Background(), readWrite())
	h.standing.lost["user-dev@"+project] = true
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v", err)
	}
	if len(h.store.keys) != 0 || len(h.secrets.secrets) != 0 {
		t.Fatal("the key is revoked, not merely refused")
	}
	h.standing.lost = map[string]bool{}
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatal("regaining the role does not bring a revoked key back")
	}
}

func TestList_RevokesKeysWhoseCreatorLostStanding(t *testing.T) {
	h := newHarness()
	_, kept, _ := h.svc.Create(context.Background(), readWrite())
	gone := readWrite()
	gone.Actor = "user-left"
	_, _, _ = h.svc.Create(context.Background(), gone)
	h.standing.lost["user-left@"+project] = true
	listed, err := h.svc.List(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != kept.ID {
		t.Fatalf("listed = %v", listed)
	}
	if len(h.store.keys) != 1 {
		t.Fatal("the creator's key is revoked")
	}
}

func TestResolve_FailuresAreNotUnknown(t *testing.T) {
	cases := map[string]func(h *harness){
		"store":    func(h *harness) { h.store.failOn = "find" },
		"standing": func(h *harness) { h.standing.err = errors.New("org store down") },
		"secrets":  func(h *harness) { h.secrets.err = errors.New("vault is sealed") },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			_, key, _ := h.svc.Create(context.Background(), readWrite())
			breakIt(h)
			_, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID)
			if err == nil || errors.Is(err, ErrUnknownKey) {
				t.Fatalf("err = %v, want a failure the caller retries", err)
			}
			if len(h.store.keys) != 1 {
				t.Fatal("a failure to check must not revoke")
			}
		})
	}
}

func TestResolve_MissingSecretIsUnknown(t *testing.T) {
	h := newHarness()
	_, key, _ := h.svc.Create(context.Background(), readWrite())
	h.secrets.secrets = map[string]string{}
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestRevoke(t *testing.T) {
	h := newHarness()
	_, key, _ := h.svc.Create(context.Background(), readWrite())
	if _, err := h.svc.Revoke(context.Background(), "other-project", key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's key: %v", err)
	}
	revoked, err := h.svc.Revoke(context.Background(), project, key.ID)
	if err != nil || revoked.ID != key.ID {
		t.Fatalf("revoke: %+v %v", revoked, err)
	}
	if len(h.secrets.secrets) != 0 {
		t.Fatal("the secret goes with the key")
	}
	if _, err := h.svc.Revoke(context.Background(), project, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestRevoke_SecretStoreFailureStillRevokes(t *testing.T) {
	h := newHarness()
	_, key, _ := h.svc.Create(context.Background(), readWrite())
	h.secrets.err = errors.New("vault is sealed")
	if _, err := h.svc.Revoke(context.Background(), project, key.ID); err != nil {
		t.Fatalf("the row is what grants access; its removal is the revocation: %v", err)
	}
	if _, _, err := h.svc.Resolve(context.Background(), key.AccessKeyID); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestPurgeProject(t *testing.T) {
	h := newHarness()
	_, _, _ = h.svc.Create(context.Background(), readWrite())
	_, _, _ = h.svc.Create(context.Background(), readWrite())
	removed, err := h.svc.PurgeProject(context.Background(), project)
	if err != nil || removed != 2 || len(h.store.keys) != 0 {
		t.Fatalf("removed %d, %v", removed, err)
	}
}

func TestKey_Allows(t *testing.T) {
	all := &Key{Permissions: Permissions{Read: true}}
	if !all.Allows("anything", OpRead) || all.Allows("anything", OpWrite) || all.Allows("anything", OpDelete) {
		t.Fatal("a read key on every bucket reads only")
	}
	scoped := &Key{Buckets: []string{"backups", "photos"}, Permissions: Permissions{Read: true, Write: true, Delete: true}}
	if !scoped.Allows("photos", OpDelete) || scoped.Allows("other", OpRead) {
		t.Fatal("a scoped key reaches only its buckets")
	}
	if !scoped.Lists("photos") || scoped.Lists("other") || !all.Lists("other") {
		t.Fatal("a key lists only buckets it reaches")
	}
}
