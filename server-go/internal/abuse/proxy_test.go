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
		{"someone/edge-ws-cover:1", "web"},
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
	}
	for _, c := range allowed {
		if got := MatchImageOrName(c.image, c.name); got != "" {
			t.Errorf("image %q name %q refused on %q", c.image, c.name, got)
		}
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
		{[]string{"NEZHA_SERVER", "NEZHA_KEY", "TROJAN_PASS"}, 3},
		{[]string{"UUID", "UUID"}, 1},
		{[]string{"ARGOCD_TOKEN"}, 0},
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
