package config

import (
	"maps"
	"strings"
	"testing"
)

func TestLoadAppRoute(t *testing.T) {
	t.Setenv("APP_DOMAIN", "apps.example.com")
	t.Setenv("APP_INGRESS_CLASS", "")
	t.Setenv("APP_TLS_SECRET", "apps-wildcard-tls")
	t.Setenv("APP_INGRESS_FROM_NAMESPACE", "haproxy-controller")
	t.Setenv("APP_INGRESS_FROM_LABELS", "app.kubernetes.io/name=kubernetes-ingress, app.kubernetes.io/instance=edge")
	cfg := Load()
	if cfg.AppDomain != "apps.example.com" || cfg.AppTLSSecret != "apps-wildcard-tls" || cfg.AppIngressFromNamespace != "haproxy-controller" {
		t.Errorf("route config = %q %q %q", cfg.AppDomain, cfg.AppTLSSecret, cfg.AppIngressFromNamespace)
	}
	if cfg.AppIngressClass != "haproxy" {
		t.Errorf("AppIngressClass default = %q, want haproxy", cfg.AppIngressClass)
	}
	want := map[string]string{"app.kubernetes.io/name": "kubernetes-ingress", "app.kubernetes.io/instance": "edge"}
	if !maps.Equal(cfg.AppIngressFromLabels, want) {
		t.Errorf("AppIngressFromLabels = %v, want %v", cfg.AppIngressFromLabels, want)
	}
}

func TestParseIngressFromLabels(t *testing.T) {
	if got, err := parseIngressFromLabels("  "); err != nil || got != nil {
		t.Errorf("empty: got %v, %v", got, err)
	}
	for _, raw := range []string{"novalue", "=x", "a=b=c", "bad key!=x", "a=bad value", "a=1,a=2"} {
		if _, err := parseIngressFromLabels(raw); err == nil {
			t.Errorf("%q must be refused", raw)
		}
	}
}

func hostingConfig() AppConfig {
	return AppConfig{
		ProvisionerMode: "k8s", DeploymentMode: "cloud", AppHostingEnabled: true,
		AppDomain: "apps.example.com", AppIngressClass: "haproxy", AppIngressFromNamespace: "haproxy-controller",
	}
}

func TestValidateAppHostingNeedsARoute(t *testing.T) {
	if err := hostingConfig().Validate(); err != nil {
		t.Fatalf("a complete hosting config must pass: %v", err)
	}
	cases := map[string]struct {
		mutate func(*AppConfig)
		want   string
	}{
		"no domain":             {func(c *AppConfig) { c.AppDomain = "" }, "APP_DOMAIN"},
		"invalid domain":        {func(c *AppConfig) { c.AppDomain = "Apps_Example" }, "APP_DOMAIN"},
		"no ingress class":      {func(c *AppConfig) { c.AppIngressClass = "" }, "APP_INGRESS_CLASS"},
		"no ingress namespace":  {func(c *AppConfig) { c.AppIngressFromNamespace = "" }, "APP_INGRESS_FROM_NAMESPACE"},
		"bad ingress namespace": {func(c *AppConfig) { c.AppIngressFromNamespace = "Not_A_Namespace" }, "APP_INGRESS_FROM_NAMESPACE"},
		"bad TLS secret name":   {func(c *AppConfig) { c.AppTLSSecret = "Bad Secret" }, "APP_TLS_SECRET"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := hostingConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %s, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateRouteIgnoredWithoutHosting(t *testing.T) {
	cfg := AppConfig{ProvisionerMode: "k8s", DeploymentMode: "cloud"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("hosting off must not require a route: %v", err)
	}
}

func TestAppDomainSettings(t *testing.T) {
	base := func() AppConfig {
		return AppConfig{AppHostingEnabled: true, AppDomain: "apps.example.com", AppIngressClass: "haproxy", AppIngressFromNamespace: "haproxy-controller"}
	}
	good := base()
	good.AppDomainIssuer, good.AppDomainResolver = "letsencrypt-prod", "10.43.0.10:53"
	if err := good.validateAppRoute(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	badIssuer := base()
	badIssuer.AppDomainIssuer = "Lets Encrypt"
	if err := badIssuer.validateAppRoute(); err == nil || !strings.Contains(err.Error(), "APP_DOMAIN_ISSUER") {
		t.Fatalf("bad issuer: %v", err)
	}
	badResolver := base()
	badResolver.AppDomainResolver = "10.43.0.10"
	if err := badResolver.validateAppRoute(); err == nil || !strings.Contains(err.Error(), "APP_DOMAIN_RESOLVER") {
		t.Fatalf("bad resolver: %v", err)
	}
}

func TestAppDomainSettingsFromEnv(t *testing.T) {
	t.Setenv("APP_DOMAIN_ISSUER", "letsencrypt-prod")
	t.Setenv("APP_DOMAIN_RESOLVER", "1.1.1.1:53")
	cfg := Load()
	if cfg.AppDomainIssuer != "letsencrypt-prod" || cfg.AppDomainResolver != "1.1.1.1:53" {
		t.Fatalf("cfg = %q %q", cfg.AppDomainIssuer, cfg.AppDomainResolver)
	}
}
