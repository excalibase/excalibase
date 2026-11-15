// Package abuse keeps the platform's free tier from being used as a proxy or
// VPN farm (EXC-755): the signatures of proxy software, the disposable and
// throwaway mail rules for sign-up, and the per-network sign-up limit. The
// lists live here only, so a later watchdog reads the same ones deploy does.
package abuse

import (
	"sort"
	"strings"
)

// proxySignatures are matched as whole tokens of an image reference or app
// name, case-insensitively. Tokens are runs of letters and digits, so
// "sing-box", "sing_box" and "singbox" are one signature and "gostbusters"
// does not contain "gost".
var proxySignatures = [][]string{
	{"xray"}, {"v2ray"}, {"vless"}, {"vmess"}, {"trojan"},
	{"sing", "box"}, {"singbox"},
	{"hysteria"}, {"hysteria2"},
	{"shadowsocks"}, {"ss", "server"},
	{"edge", "ws"}, {"ws", "runtime"},
	{"eooce"}, {"pxnode"}, {"nezha"}, {"nezhahq"}, {"gost"},
	{"naiveproxy"}, {"naive", "proxy"},
	{"tuic"}, {"brook"},
}

// proxyEnvNames are environment variable names a proxy node is configured by.
var proxyEnvNames = map[string]bool{
	"UUID": true, "NODE_UUID": true, "WS_PATH": true, "WSPATH": true, "STREAM_PATH": true,
}

var proxyEnvPrefixes = []string{"VLESS_", "VMESS_", "XRAY_", "ARGO_", "NEZHA_", "TROJAN_"}

// MinProxyEnvKeys is how many proxy-shaped variables make an app a proxy.
const MinProxyEnvKeys = 2

// Finding says why an app was taken for a proxy.
type Finding struct {
	// Signature is the matched image or name signature, or empty.
	Signature string
	// EnvKeys are the proxy-shaped variable names, sorted, when that rule matched.
	EnvKeys []string
}

// Reason is a short description for the error and the audit entry.
func (f Finding) Reason() string {
	if f.Signature != "" {
		return "image or name matches the proxy signature " + f.Signature
	}
	return "environment has proxy settings (" + strings.Join(f.EnvKeys, ", ") + ")"
}

func tokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

func hasRun(haystack, needle []string) bool {
	for start := 0; start+len(needle) <= len(haystack); start++ {
		match := true
		for i, word := range needle {
			if haystack[start+i] != word {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// MatchImageOrName returns the signature an image reference or app name
// carries, or "".
func MatchImageOrName(image, name string) string {
	for _, text := range []string{image, name} {
		words := tokens(text)
		for _, signature := range proxySignatures {
			if hasRun(words, signature) {
				return strings.Join(signature, "-")
			}
		}
	}
	return ""
}

// ProxyEnvKeys returns the distinct proxy-shaped names among keys, upper-cased and sorted.
func ProxyEnvKeys(keys []string) []string {
	seen := map[string]bool{}
	for _, key := range keys {
		upper := strings.ToUpper(strings.TrimSpace(key))
		if proxyEnvNames[upper] {
			seen[upper] = true
			continue
		}
		for _, prefix := range proxyEnvPrefixes {
			if strings.HasPrefix(upper, prefix) {
				seen[upper] = true
				break
			}
		}
	}
	found := make([]string, 0, len(seen))
	for key := range seen {
		found = append(found, key)
	}
	sort.Strings(found)
	return found
}

// FindProxy decides whether an app is a proxy node: its image or name carries
// a signature, or its environment holds MinProxyEnvKeys proxy-shaped names.
func FindProxy(image, name string, envNames []string) *Finding {
	if signature := MatchImageOrName(image, name); signature != "" {
		return &Finding{Signature: signature}
	}
	if keys := ProxyEnvKeys(envNames); len(keys) >= MinProxyEnvKeys {
		return &Finding{EnvKeys: keys}
	}
	return nil
}
