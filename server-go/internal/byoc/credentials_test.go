package byoc

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateHostSyntax_Rejected(t *testing.T) {
	rejected := []struct{ name, host string }{
		{"empty", ""},
		{"blank", "   "},
		{"multi-host comma", "a.example.com,b.example.com"},
		{"hostaddr injected via space", "a.example.com hostaddr=169.254.169.254"},
		{"host injected via space", "a.example.com host=127.0.0.1"},
		{"tab separated", "a.example.com\thost=127.0.0.1"},
		{"equals sign", "host=127.0.0.1"},
		{"embedded port", "a.example.com:5432"},
		{"url form", "postgres://a.example.com/db"},
		{"path", "a.example.com/db"},
		{"query", "a.example.com?host=127.0.0.1"},
		{"userinfo", "user@a.example.com"},
		{"quote", "a.example.com'"},
		{"backslash", "a.example.com\\"},
		{"ipv6 zone id", "fe80::1%eth0"},
		{"bracketed ipv6 zone id", "[fe80::1%25eth0]"},
		{"control char", "a.example.com\x00"},
		{"unix socket dir", "/var/run/postgresql"},
		{"leading dot", ".example.com"},
		{"double dot", "a..example.com"},
		{"leading hyphen label", "-a.example.com"},
		{"too long label", strings.Repeat("a", 64) + ".example.com"},
		{"too long name", strings.Repeat("a.", 130) + "com"},
		{"underscore label", "my_db.example.com"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHostSyntax(tc.host)
			if !errors.Is(err, ErrInvalidHost) {
				t.Fatalf("ValidateHostSyntax(%q) = %v, want ErrInvalidHost", tc.host, err)
			}
		})
	}
}

func TestValidateHostSyntax_Accepted(t *testing.T) {
	accepted := []string{
		"db.example.com",
		"DB.Example.COM",
		"my-host.acme.io",
		"db.example.com.",
		"8.8.8.8",
		"2001:4860:4860::8888",
		"[2001:4860:4860::8888]",
		"a1.b2.c3",
		"xn--bcher-kva.example",
	}
	for _, host := range accepted {
		if err := ValidateHostSyntax(host); err != nil {
			t.Errorf("ValidateHostSyntax(%q) = %v, want nil", host, err)
		}
	}
}

func TestCredentials_Validate_RejectsDSNMetacharacters(t *testing.T) {
	base := Credentials{Host: "db.example.com", Database: "app", Username: "app_user", Password: "s3cret"}
	rejected := []struct {
		name   string
		mutate func(c *Credentials)
	}{
		{"username host override", func(c *Credentials) { c.Username = "app_user host=127.0.0.1" }},
		{"username hostaddr override", func(c *Credentials) { c.Username = "app_user hostaddr=10.0.0.1" }},
		{"database override", func(c *Credentials) { c.Database = "app dbname=x host=127.0.0.1" }},
		{"password kv override", func(c *Credentials) { c.Password = "x host=127.0.0.1" }},
		{"password equals", func(c *Credentials) { c.Password = "a=b" }},
		{"password quote", func(c *Credentials) { c.Password = "a'b" }},
		{"password backslash", func(c *Credentials) { c.Password = `a\b` }},
		{"password url path", func(c *Credentials) { c.Password = "a/b" }},
		{"password url query", func(c *Credentials) { c.Password = "a?host=x" }},
		{"password url fragment", func(c *Credentials) { c.Password = "a#b" }},
		{"password control char", func(c *Credentials) { c.Password = "a\nb" }},
		{"username url userinfo", func(c *Credentials) { c.Username = "a@evil" }},
		{"username colon", func(c *Credentials) { c.Username = "a:b" }},
		{"database slash", func(c *Credentials) { c.Database = "a/b" }},
		{"database percent", func(c *Credentials) { c.Database = "a%2fb" }},
		{"empty username", func(c *Credentials) { c.Username = "" }},
		{"empty database", func(c *Credentials) { c.Database = "" }},
		{"empty password", func(c *Credentials) { c.Password = "" }},
		{"multi-host", func(c *Credentials) { c.Host = "a.example.com,b.example.com" }},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() should reject %+v", c)
			}
			if !errors.Is(err, ErrInvalidCredential) && !errors.Is(err, ErrInvalidHost) {
				t.Fatalf("Validate() = %v, want ErrInvalidCredential or ErrInvalidHost", err)
			}
		})
	}
}

func TestCredentials_Validate_Accepted(t *testing.T) {
	ok := []Credentials{
		{Host: "db.example.com", Database: "app", Username: "app_user", Password: "s3cret"},
		{Host: "8.8.8.8", Database: "app-prod", Username: "app.user", Password: "p@ss!w0rd$^&*()[]{}~+-_.,;|<>`\""},
		{Host: "[2001:4860:4860::8888]", Database: "d1", Username: "u1", Password: "Ünïcödé"},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", c, err)
		}
	}
}

func TestCredentials_Validate_ErrorDoesNotEchoPassword(t *testing.T) {
	c := Credentials{Host: "db.example.com", Database: "app", Username: "u", Password: "hunter2 host=127.0.0.1"}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error must not echo the password: %v", err)
	}
}
