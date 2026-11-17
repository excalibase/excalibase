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
// does not contain "gost". Words that real software shares (edge-ws, brook,
// ss-server, a bare xray) are not here; see exactNames and ambiguousSignatures.
var proxySignatures = [][]string{
	{"xray", "core"}, {"teddysun", "xray"}, {"docker", "xray"}, {"xtls"},
	{"v2ray"}, {"vless"}, {"vmess"}, {"trojan"},
	{"sing", "box"}, {"singbox"},
	{"hysteria"}, {"hysteria2"},
	{"shadowsocks"},
	{"edge", "ws", "cover"}, {"edgerelay2610"}, {"ws", "runtime"},
	{"eooce"}, {"pxnode"}, {"nezha"}, {"mupeng111"}, {"nezhahq"}, {"gost"},
	{"naiveproxy"}, {"naive", "proxy"},
	{"tuic"}, {"txthinking", "brook"},
}

// ambiguousSignatures name proxy software that other software shares a word
// with: Amazon's X-Ray daemon and JFrog Xray are not proxies.
var ambiguousSignatures = []struct {
	signature []string
	unless    []string
}{
	{signature: []string{"xray"}, unless: []string{"aws", "amazon", "jfrog"}},
}

// exactNames are matched against the whole app name or the whole last path
// segment of the image repository, so brook-scheduler and ss-server-metrics pass.
var exactNames = []string{"brook", "excalibase-node", "excalibase_node", "ss-server", "ss_server", "ssserver"}

// proxyEnvNames are environment variable names a proxy node is configured by.
// Argo Workflows and Argo CD read ARGO_* too, so only the tunnel's own are named.
var proxyEnvNames = map[string]bool{
	"UUID": true, "NODE_UUID": true, "WS_PATH": true, "WSPATH": true, "STREAM_PATH": true,
	"ARGO_AUTH": true, "ARGO_DOMAIN": true, "ARGO_PORT": true,
	"SUB_TOKEN": true, "SUB_PATH": true,
}

// uuidEnvNames name the node identity of a proxy. TRANSPORT alone is an
// ordinary word (mail, queues), so it counts only beside one of these.
var uuidEnvNames = map[string]bool{"UUID": true, "NODE_UUID": true}

var proxyEnvPrefixes = []string{"VLESS_", "VMESS_", "XRAY_", "NEZHA_", "TROJAN_"}

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

func hasAny(words, wanted []string) bool {
	for _, word := range words {
		for _, candidate := range wanted {
			if word == candidate {
				return true
			}
		}
	}
	return false
}

// repositoryName is the last path segment of an image reference without its tag or digest.
func repositoryName(image string) string {
	image = strings.ToLower(strings.TrimSpace(image))
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	if slash := strings.LastIndexByte(image, '/'); slash >= 0 {
		image = image[slash+1:]
	}
	if colon := strings.IndexByte(image, ':'); colon >= 0 {
		image = image[:colon]
	}
	return image
}

// matchText looks for a signature in text. A software's own image is what
// says an ambiguous word is not a proxy, so a name is judged with the image's
// words too, and an image only with its own.
func matchText(text string, alongside []string) string {
	words := tokens(text)
	for _, signature := range proxySignatures {
		if hasRun(words, signature) {
			return strings.Join(signature, "-")
		}
	}
	for _, ambiguous := range ambiguousSignatures {
		if hasRun(words, ambiguous.signature) && !hasAny(words, ambiguous.unless) && !hasAny(alongside, ambiguous.unless) {
			return strings.Join(ambiguous.signature, "-")
		}
	}
	return ""
}

// MatchImageOrName returns the signature an image reference or app name
// carries, or "".
func MatchImageOrName(image, name string) string {
	if signature := matchText(image, nil); signature != "" {
		return signature
	}
	if signature := matchText(name, tokens(image)); signature != "" {
		return signature
	}
	whole := []string{repositoryName(image), strings.ToLower(strings.TrimSpace(name))}
	for _, candidate := range whole {
		for _, exact := range exactNames {
			if candidate == exact {
				return exact
			}
		}
	}
	return ""
}

func hasProxyPrefix(upper string) bool {
	for _, prefix := range proxyEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// ProxyEnvKeys returns the distinct proxy-shaped names among keys, upper-cased and sorted.
func ProxyEnvKeys(keys []string) []string {
	seen := map[string]bool{}
	hasUUID := false
	for _, key := range keys {
		upper := strings.ToUpper(strings.TrimSpace(key))
		if uuidEnvNames[upper] || (hasProxyPrefix(upper) && strings.HasSuffix(upper, "_UUID")) {
			hasUUID = true
		}
	}
	for _, key := range keys {
		upper := strings.ToUpper(strings.TrimSpace(key))
		if upper == "TRANSPORT" && hasUUID {
			seen[upper] = true
			continue
		}
		if proxyEnvNames[upper] {
			seen[upper] = true
			continue
		}
		if hasProxyPrefix(upper) {
			seen[upper] = true
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
