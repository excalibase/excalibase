package email

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server: enough of RFC 5321 for the sender to
// greet, upgrade, authenticate and hand over one message.
type fakeSMTP struct {
	listener     net.Listener
	tlsConfig    *tls.Config
	advertiseTLS bool
	rejectRcpt   string // a 550 reply to RCPT TO for this address

	mu        sync.Mutex
	authSeen  []string
	authOnTLS bool
	mailFrom  string
	rcpts     []string
	data      string
}

func newTestCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtp.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, caFile
}

// startFakeSMTP listens on 127.0.0.1. implicitTLS wraps the listener in TLS
// (port-465 style); advertiseTLS offers STARTTLS on a plain listener.
func startFakeSMTP(t *testing.T, cert tls.Certificate, implicitTLS, advertiseTLS bool) *fakeSMTP {
	t.Helper()
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	var listener net.Listener
	var err error
	if implicitTLS {
		listener, err = tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	} else {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSMTP{listener: listener, tlsConfig: tlsConfig, advertiseTLS: advertiseTLS}
	t.Cleanup(func() { _ = listener.Close() })
	go server.serve(implicitTLS)
	return server
}

func (f *fakeSMTP) port() int { return f.listener.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(implicitTLS bool) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn, implicitTLS)
	}
}

func (f *fakeSMTP) handle(conn net.Conn, onTLS bool) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 smtp.test ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			lines := []string{"250-smtp.test"}
			if f.advertiseTLS && !onTLS {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 AUTH PLAIN")
			for _, l := range lines {
				reply(l)
			}
		case upper == "STARTTLS":
			reply("220 go ahead")
			tlsConn := tls.Server(conn, f.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
			onTLS = true
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			f.mu.Lock()
			f.authSeen = append(f.authSeen, command)
			f.authOnTLS = onTLS
			f.mu.Unlock()
			reply("235 ok")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.mailFrom = command[len("MAIL FROM:"):]
			f.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			to := command[len("RCPT TO:"):]
			if f.rejectRcpt != "" && strings.Contains(to, f.rejectRcpt) {
				reply("550 5.1.1 no such user")
				continue
			}
			f.mu.Lock()
			f.rcpts = append(f.rcpts, to)
			f.mu.Unlock()
			reply("250 ok")
		case upper == "DATA":
			reply("354 send it")
			var body strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				body.WriteString(dataLine)
			}
			f.mu.Lock()
			f.data = body.String()
			f.mu.Unlock()
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (f *fakeSMTP) snapshot() (authSeen []string, authOnTLS bool, rcpts []string, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authSeen...), f.authOnTLS, append([]string(nil), f.rcpts...), f.data
}

func testMessage() Message {
	return Message{
		To:       []string{"user@example.com"},
		Subject:  "Verify your email",
		HTMLBody: "<p>Click <a href=\"https://studio.test/verify?t=abc\">here</a></p>",
		TextBody: "Open https://studio.test/verify?t=abc",
	}
}

func TestSMTPSender_StartTLSIsTheDefaultAndAuthRunsOverTLS(t *testing.T) {
	cert, caFile := newTestCert(t)
	server := startFakeSMTP(t, cert, false, true)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), Username: "mailer", Password: "s3cret-pass",
		CAFile: caFile, DefaultFrom: "noreply@excalibase.test", DefaultFromName: "Excalibase",
	})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	defer sender.Stop()

	id, err := sender.Send(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == "" {
		t.Error("Send returned an empty message id")
	}
	authSeen, authOnTLS, rcpts, data := server.snapshot()
	if len(authSeen) != 1 || !authOnTLS {
		t.Fatalf("credentials must go over TLS: auth=%v onTLS=%v", authSeen, authOnTLS)
	}
	encoded := strings.TrimSpace(strings.TrimPrefix(authSeen[0], "AUTH PLAIN"))
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	if string(decoded) != "\x00mailer\x00s3cret-pass" {
		t.Errorf("auth payload = %q", decoded)
	}
	if len(rcpts) != 1 || !strings.Contains(rcpts[0], "user@example.com") {
		t.Errorf("rcpts = %v", rcpts)
	}
	for _, want := range []string{
		`From: "Excalibase" <noreply@excalibase.test>`,
		"To: <user@example.com>",
		"Subject: Verify your email",
		"Message-Id: " + id,
		"multipart/alternative",
		"text/plain",
		"text/html",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message lacks %q:\n%s", want, data)
		}
	}
}

func TestSMTPSender_RefusesAServerThatDoesNotOfferStartTLS(t *testing.T) {
	cert, caFile := newTestCert(t)
	server := startFakeSMTP(t, cert, false, false)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), Username: "mailer", Password: "s3cret-pass",
		CAFile: caFile, DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	defer sender.Stop()

	_, err = sender.Send(context.Background(), testMessage())
	if err == nil {
		t.Fatal("Send succeeded without TLS")
	}
	if strings.Contains(err.Error(), "s3cret-pass") {
		t.Error("the error leaks the password")
	}
	if authSeen, _, _, _ := server.snapshot(); len(authSeen) != 0 {
		t.Errorf("credentials were sent on a plain connection: %v", authSeen)
	}
}

func TestSMTPSender_RefusesAnUntrustedCertificate(t *testing.T) {
	cert, _ := newTestCert(t)
	server := startFakeSMTP(t, cert, false, true)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), Username: "mailer", Password: "s3cret-pass",
		DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	defer sender.Stop()

	if _, err := sender.Send(context.Background(), testMessage()); err == nil {
		t.Fatal("Send trusted a certificate no CA vouches for")
	}
	if authSeen, _, _, _ := server.snapshot(); len(authSeen) != 0 {
		t.Errorf("credentials were sent before the certificate was verified: %v", authSeen)
	}
}

func TestSMTPSender_ImplicitTLS(t *testing.T) {
	cert, caFile := newTestCert(t)
	server := startFakeSMTP(t, cert, true, false)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), TLSMode: SMTPTLSImplicit, Username: "mailer", Password: "pw",
		CAFile: caFile, DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	defer sender.Stop()

	if _, err := sender.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, authOnTLS, rcpts, _ := server.snapshot(); !authOnTLS || len(rcpts) != 1 {
		t.Errorf("implicit TLS send: onTLS=%v rcpts=%v", authOnTLS, rcpts)
	}
}

func TestSMTPSender_PlainModeSendsWithoutCredentials(t *testing.T) {
	cert, _ := newTestCert(t)
	server := startFakeSMTP(t, cert, false, false)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), TLSMode: SMTPTLSNone, DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	defer sender.Stop()

	if _, err := sender.Send(context.Background(), testMessage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, _, rcpts, _ := server.snapshot(); len(rcpts) != 1 {
		t.Errorf("rcpts = %v", rcpts)
	}
}

func TestNewSMTPSender_RefusesBadConfig(t *testing.T) {
	cases := map[string]SMTPConfig{
		"no host":                   {Port: 587, DefaultFrom: "a@b.test"},
		"no from":                   {Host: "smtp.test", Port: 587},
		"unknown tls mode":          {Host: "smtp.test", Port: 587, TLSMode: "maybe", DefaultFrom: "a@b.test"},
		"password over plain":       {Host: "smtp.test", Port: 25, TLSMode: SMTPTLSNone, Username: "u", Password: "p", DefaultFrom: "a@b.test"},
		"username without password": {Host: "smtp.test", Port: 587, Username: "u", DefaultFrom: "a@b.test"},
		"missing ca file":           {Host: "smtp.test", Port: 587, CAFile: "/nonexistent/ca.pem", DefaultFrom: "a@b.test"},
		"bad port":                  {Host: "smtp.test", Port: 70000, DefaultFrom: "a@b.test"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewSMTPSender(cfg); err == nil {
				t.Error("config accepted")
			}
		})
	}
}

func TestSMTPSender_RejectsHeaderInjection(t *testing.T) {
	cert, _ := newTestCert(t)
	server := startFakeSMTP(t, cert, false, false)
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), TLSMode: SMTPTLSNone, DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Stop()

	injected := testMessage()
	injected.To = []string{"user@example.com\r\nBcc: victim@example.com"}
	if _, err := sender.Send(context.Background(), injected); !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("injected recipient: err = %v, want ErrInvalidRecipient", err)
	}
	subject := testMessage()
	subject.Subject = "hi\r\nBcc: victim@example.com"
	if _, err := sender.Send(context.Background(), subject); err == nil {
		t.Error("a subject carrying a header line was sent")
	}
	if _, _, rcpts, _ := server.snapshot(); len(rcpts) != 0 {
		t.Errorf("injected messages reached the server: %v", rcpts)
	}
}

func TestSMTPSender_MapsRejectedRecipient(t *testing.T) {
	cert, _ := newTestCert(t)
	server := startFakeSMTP(t, cert, false, false)
	server.rejectRcpt = "ghost@example.com"
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: server.port(), TLSMode: SMTPTLSNone, DefaultFrom: "noreply@excalibase.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Stop()

	msg := testMessage()
	msg.To = []string{"ghost@example.com"}
	if _, err := sender.Send(context.Background(), msg); !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("err = %v, want ErrInvalidRecipient", err)
	}
}

func TestSMTPSender_ReportsAnUnreachableServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	sender, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: port, TLSMode: SMTPTLSNone, DefaultFrom: "noreply@excalibase.test",
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Stop()

	_, err = sender.Send(context.Background(), testMessage())
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Errorf("err = %v, want a dial error naming the address", err)
	}
}

func TestNewSMTPSender_PortFollowsTheTLSMode(t *testing.T) {
	for mode, want := range map[string]int{"": 587, SMTPTLSStartTLS: 587, SMTPTLSImplicit: 465, SMTPTLSNone: 25} {
		sender, err := NewSMTPSender(SMTPConfig{Host: "smtp.test", TLSMode: mode, DefaultFrom: "a@b.test"})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if sender.cfg.Port != want {
			t.Errorf("mode %q: port %d, want %d", mode, sender.cfg.Port, want)
		}
		sender.Stop()
	}
}
