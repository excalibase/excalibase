package abuse

import "testing"

func TestMatchImageOrName_Signatures(t *testing.T) {
	refused := []struct{ image, name string }{
		{"ghcr.io/xtls/xray-core:latest", "web"},
		{"teddysun/v2ray", "web"},
		{"node:22", "my-vless-node"},
		{"docker.io/someone/vmess-ws:1", "web"},
		{"trojan-go:1", "web"},
		{"ghcr.io/sagernet/sing-box:v1", "web"},
		{"ghcr.io/sagernet/singbox", "web"},
		{"apernet/hysteria2", "web"},
		{"shadowsocks/shadowsocks-libev", "web"},
		{"someone/ss-server", "web"},
		{"edgerelay2610/edge-ws-cover:1", "web"},
		{"someone/edge-ws-cover", "web"},
		{"teddysun/xray", "web"},
		{"iamybj/docker-xray:latest", "web"},
		{"xtls/xray-core", "web"},
		{"ghcr.io/xtls/anything:1", "web"},
		{"txthinking/brook", "web"},
		{"someone/ss-server:1", "web"},
		{"nginx:1", "ss-server"},
		{"nginx:1", "brook"},
		{"nginx:1", "xray"},
		{"someone/ws-runtime", "web"},
		{"eooce/node", "web"},
		{"someone/pxnode", "web"},
		{"nezhahq/agent", "web"},
		{"ginuerzh/gost", "web"},
		{"someone/naiveproxy", "web"},
		{"someone/tuic-server", "web"},
		{"someone/brook", "web"},
		{"XRAY:Latest", "web"},
		{"nginx:1", "Xray_Node"},
		{"nginx:1", "sing_box"},
		{"ghcr.io/mupeng111/excalibase-node:latest", "web"},
		{"ghcr.io/mupeng111/anything:1", "web"},
		{"nginx:1", "excalibase-node"},
		{"nginx:1", "excalibase_node"},
		{"ghcr.io:443/mupeng111/x:1", "web"},
		{"mupeng111/x:1", "web"},
		{"quay.io/acme/excalibase-node:1", "web"},
	}
	for _, c := range refused {
		if got := MatchImageOrName(c.image, c.name); got == "" {
			t.Errorf("image %q name %q was allowed", c.image, c.name)
		}
	}
}

func TestMatchImageOrName_NormalAppsPass(t *testing.T) {
	allowed := []struct{ image, name string }{
		{"node:22", "api"},
		{"ghcr.io/acme/shop-backend:main", "web"},
		{"postgres:16", "db"},
		{"nginx:1.27-alpine", "static-site"},
		{"someone/brooklyn-news", "gostbusters"},
		{"someone/tuiclient", "xrayless"},
		{"redis:7", "sing-along"},
		{"python:3.12", "ssh-bastion"},
		{"ghcr.io/otherowner/app:1", "web"},
		{"ghcr.io/mupeng1110/x:1", "web"},
		{"ghcr.io/mupeng11/x:1", "web"},
		{"someone/excalibase-node-exporter", "excalibase-node-exporter"},
	}
	for _, c := range allowed {
		if got := MatchImageOrName(c.image, c.name); got != "" {
			t.Errorf("image %q name %q refused on %q", c.image, c.name, got)
		}
	}
}

// Software that shares a word with a proxy is not a proxy (EXC-755 review).
func TestMatchImageOrName_FalsePositivesPass(t *testing.T) {
	allowed := []struct{ image, name string }{
		{"amazon/aws-xray-daemon", "tracing"},
		{"public.ecr.aws/xray/aws-xray-daemon:3", "tracing"},
		{"jfrog/xray-server", "xray-server"},
		{"releases-docker.jfrog.io/jfrog/xray-server:3", "scanner"},
		{"nginx:1", "aws-xray"},
		{"someone/edge-ws-gateway", "web"},
		{"nginx:1", "edge-ws-gateway"},
		{"someone/ss-server-metrics", "web"},
		{"nginx:1", "ss-server-metrics"},
		{"someone/brook-scheduler", "web"},
		{"nginx:1", "brook-scheduler"},
	}
	for _, c := range allowed {
		if got := MatchImageOrName(c.image, c.name); got != "" {
			t.Errorf("image %q name %q refused on %q", c.image, c.name, got)
		}
	}
}

func TestFindProxy_ArgoWorkflowsEnvironmentsPass(t *testing.T) {
	for _, keys := range [][]string{
		{"ARGO_SERVER", "ARGO_TOKEN"},
		{"UUID", "ARGO_NAMESPACE"},
		{"ARGO_BASE_HREF", "ARGO_NAMESPACE", "ARGO_SECURE"},
	} {
		if f := FindProxy("quay.io/argoproj/argocli", "workflows", keys); f != nil {
			t.Errorf("%v refused: %s", keys, f.Reason())
		}
	}
	if f := FindProxy("node:22", "api", []string{"ARGO_AUTH", "ARGO_DOMAIN"}); f == nil {
		t.Error("the tunnel's ARGO_AUTH and ARGO_DOMAIN were allowed")
	}
	if f := FindProxy("node:22", "api", []string{"UUID", "ARGO_AUTH"}); f == nil {
		t.Error("UUID with ARGO_AUTH was allowed")
	}
}

func TestProxyEnvKeys(t *testing.T) {
	cases := []struct {
		keys []string
		want int
	}{
		{[]string{"DATABASE_URL", "PORT"}, 0},
		{[]string{"UUID"}, 1},
		{[]string{"UUID", "PORT"}, 1},
		{[]string{"uuid", "ws_path"}, 2},
		{[]string{"NODE_UUID", "WSPATH"}, 2},
		{[]string{"UUID", "STREAM_PATH", "PORT"}, 2},
		{[]string{"VLESS_PORT", "VMESS_ID"}, 2},
		{[]string{"XRAY_CONFIG", "ARGO_AUTH"}, 2},
		{[]string{"ARGO_DOMAIN", "argo_auth"}, 2},
		{[]string{"NEZHA_SERVER", "NEZHA_KEY", "TROJAN_PASS"}, 3},
		{[]string{"UUID", "UUID"}, 1},
		{[]string{"ARGOCD_TOKEN", "ARGO_SERVER", "ARGO_TOKEN", "ARGO_NAMESPACE"}, 0},
	}
	for _, c := range cases {
		if got := len(ProxyEnvKeys(c.keys)); got != c.want {
			t.Errorf("%v: %d proxy keys, want %d", c.keys, got, c.want)
		}
	}
}

func TestFindProxy_ByEnvNeedsTwoKeys(t *testing.T) {
	if f := FindProxy("node:22", "api", []string{"DATABASE_URL", "PORT"}); f != nil {
		t.Fatalf("normal app refused: %+v", f)
	}
	if f := FindProxy("node:22", "api", []string{"UUID", "PORT"}); f != nil {
		t.Fatalf("one proxy-shaped key refused: %+v", f)
	}
	f := FindProxy("node:22", "api", []string{"UUID", "WS_PATH"})
	if f == nil || len(f.EnvKeys) != 2 {
		t.Fatalf("two proxy keys allowed: %+v", f)
	}
	f = FindProxy("xray:1", "api", nil)
	if f == nil || f.Signature == "" {
		t.Fatalf("image signature allowed: %+v", f)
	}
}
