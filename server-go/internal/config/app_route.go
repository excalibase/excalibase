package config

import (
	"fmt"
	"log"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// parseIngressFromLabels reads "key=value,key=value" into the pod labels the app ingress fence admits.
func parseIngressFromLabels(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	labels := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(pair), "=")
		if !found {
			return nil, fmt.Errorf("%q is not key=value", pair)
		}
		problems := append(validation.IsQualifiedName(key), validation.IsValidLabelValue(value)...)
		if len(problems) > 0 {
			return nil, fmt.Errorf("%q: %s", pair, strings.Join(problems, "; "))
		}
		if _, dup := labels[key]; dup {
			return nil, fmt.Errorf("label %q is given twice", key)
		}
		labels[key] = value
	}
	return labels, nil
}

func envIngressFromLabels(key string) map[string]string {
	labels, err := parseIngressFromLabels(os.Getenv(key))
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return labels
}

type routeSetting struct {
	env, value string
	check      func(string) []string
}

// validateAppRoute: every app gets a hostname under APP_DOMAIN, and its fence admits only the ingress controller.
func (c AppConfig) validateAppRoute() error {
	if !c.AppHostingEnabled {
		return nil
	}
	settings := []routeSetting{
		{"APP_DOMAIN", c.AppDomain, validation.IsDNS1123Subdomain},
		{"APP_INGRESS_CLASS", c.AppIngressClass, validation.IsDNS1123Subdomain},
		{"APP_INGRESS_FROM_NAMESPACE", c.AppIngressFromNamespace, validation.IsDNS1123Label},
	}
	if c.AppTLSSecret != "" {
		settings = append(settings, routeSetting{"APP_TLS_SECRET", c.AppTLSSecret, validation.IsDNS1123Subdomain})
	}
	for _, setting := range settings {
		if setting.value == "" {
			return fmt.Errorf("APP_HOSTING_ENABLED needs %s", setting.env)
		}
		if problems := setting.check(setting.value); len(problems) > 0 {
			return fmt.Errorf("%s %q: %s", setting.env, setting.value, strings.Join(problems, "; "))
		}
	}
	return nil
}
