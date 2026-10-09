package engineproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPolicy() Policy {
	return Policy{
		ManagedLabel:    "excalibase.managed",
		Networks:        []string{"excalibase-tenants"},
		NetworkPrefix:   "excalibase-proj-",
		PortBindIPs:     []string{"127.0.0.1"},
		VolumePrefix:    "excalibase-proj-",
		Runtimes:        []string{"runsc"},
		ContainerPrefix: "excalibase-",
	}
}

// createBody is what the Docker SDK sends for a tenant database: every
// HostConfig field present, most at their zero value.
func createBody(t *testing.T, mutate func(body map[string]any)) []byte {
	t.Helper()
	host := map[string]any{
		"Binds": nil, "Privileged": false, "CapAdd": nil, "CapDrop": nil,
		"NetworkMode": "", "PidMode": "", "IpcMode": "", "UTSMode": "", "UsernsMode": "",
		"CgroupnsMode": "", "SecurityOpt": nil, "Devices": nil, "DeviceRequests": nil,
		"VolumesFrom": nil, "Sysctls": nil, "Mounts": nil, "Runtime": "",
		"PortBindings":  map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}},
		"RestartPolicy": map[string]any{"Name": "unless-stopped", "MaximumRetryCount": 0},
		"Memory":        0, "NanoCpus": 0, "MaskedPaths": nil, "ReadonlyPaths": nil,
		"OomKillDisable": nil, "CgroupParent": "", "Cgroup": "",
		"LogConfig": map[string]any{"Type": "", "Config": nil},
	}
	body := map[string]any{
		"Image":      "postgres:17",
		"Env":        []any{"POSTGRES_DB=app"},
		"Labels":     map[string]any{"excalibase.managed": "true"},
		"HostConfig": host,
		"NetworkingConfig": map[string]any{
			"EndpointsConfig": map[string]any{"excalibase-tenants": map[string]any{}},
		},
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func hostConfig(body map[string]any) map[string]any { return body["HostConfig"].(map[string]any) }

func TestCheckCreateAllowsTheTenantDatabaseTheProvisionerSends(t *testing.T) {
	if err := testPolicy().CheckCreate("excalibase-p1-postgres", createBody(t, nil)); err != nil {
		t.Fatalf("tenant create refused: %v", err)
	}
}

func TestCheckCreateRefuses(t *testing.T) {
	cases := map[string]struct {
		name   string
		mutate func(body map[string]any)
		want   string
	}{
		"no managed label":           {mutate: func(b map[string]any) { b["Labels"] = map[string]any{} }, want: "label"},
		"name outside the prefix":    {name: "excalibase", want: "name"},
		"name of a platform service": {name: "provisioning", want: "name"},
		"privileged":                 {mutate: func(b map[string]any) { hostConfig(b)["Privileged"] = true }, want: "Privileged"},
		"added capability":           {mutate: func(b map[string]any) { hostConfig(b)["CapAdd"] = []any{"SYS_ADMIN"} }, want: "CapAdd"},
		"host path bind":             {mutate: func(b map[string]any) { hostConfig(b)["Binds"] = []any{"/:/host"} }, want: "bind"},
		"relative host path bind":    {mutate: func(b map[string]any) { hostConfig(b)["Binds"] = []any{"./x:/host"} }, want: "bind"},
		"bind mount": {mutate: func(b map[string]any) {
			hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "bind", "Source": "/etc", "Target": "/x"}}
		}, want: "mount"},
		"volume with driver options": {mutate: func(b map[string]any) {
			hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "data", "Target": "/x",
				"VolumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]any{"o": "bind", "device": "/"}}}}}
		}, want: "mount"},
		"device":                {mutate: func(b map[string]any) { hostConfig(b)["Devices"] = []any{map[string]any{"PathOnHost": "/dev/sda"}} }, want: "Devices"},
		"host pid namespace":    {mutate: func(b map[string]any) { hostConfig(b)["PidMode"] = "host" }, want: "PidMode"},
		"another container pid": {mutate: func(b map[string]any) { hostConfig(b)["PidMode"] = "container:x" }, want: "PidMode"},
		"host ipc":              {mutate: func(b map[string]any) { hostConfig(b)["IpcMode"] = "host" }, want: "IpcMode"},
		"host uts":              {mutate: func(b map[string]any) { hostConfig(b)["UTSMode"] = "host" }, want: "UTSMode"},
		"host userns":           {mutate: func(b map[string]any) { hostConfig(b)["UsernsMode"] = "host" }, want: "UsernsMode"},
		"host cgroupns":         {mutate: func(b map[string]any) { hostConfig(b)["CgroupnsMode"] = "host" }, want: "CgroupnsMode"},
		"host network":          {mutate: func(b map[string]any) { hostConfig(b)["NetworkMode"] = "host" }, want: "network"},
		"platform network mode": {mutate: func(b map[string]any) { hostConfig(b)["NetworkMode"] = "excalibase-platform" }, want: "network"},
		"platform network endpoint": {mutate: func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-platform": map[string]any{}}}
		}, want: "network"},
		"unconfined seccomp": {mutate: func(b map[string]any) { hostConfig(b)["SecurityOpt"] = []any{"seccomp=unconfined"} }, want: "SecurityOpt"},
		"selinux label off":  {mutate: func(b map[string]any) { hostConfig(b)["SecurityOpt"] = []any{"label=disable"} }, want: "SecurityOpt"},
		"unmasked proc":      {mutate: func(b map[string]any) { hostConfig(b)["MaskedPaths"] = []any{} }, want: "MaskedPaths"},
		"sysctl":             {mutate: func(b map[string]any) { hostConfig(b)["Sysctls"] = map[string]any{"kernel.x": "1"} }, want: "Sysctls"},
		"volumes from":       {mutate: func(b map[string]any) { hostConfig(b)["VolumesFrom"] = []any{"excalibase-provisioning"} }, want: "VolumesFrom"},
		"unknown runtime":    {mutate: func(b map[string]any) { hostConfig(b)["Runtime"] = "custom" }, want: "Runtime"},
		"port on every address": {mutate: func(b map[string]any) {
			hostConfig(b)["PortBindings"] = map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "0.0.0.0", "HostPort": "5432"}}}
		}, want: "port"},
		"port with no address": {mutate: func(b map[string]any) {
			hostConfig(b)["PortBindings"] = map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "", "HostPort": "5432"}}}
		}, want: "port"},
		"syslog to a remote host": {mutate: func(b map[string]any) {
			hostConfig(b)["LogConfig"] = map[string]any{"Type": "syslog", "Config": map[string]any{"syslog-address": "tcp://x"}}
		}, want: "LogConfig"},
		"a field the proxy does not know": {mutate: func(b map[string]any) { hostConfig(b)["FutureEscape"] = true }, want: "FutureEscape"},
		"a platform volume by bind": {mutate: func(b map[string]any) {
			hostConfig(b)["Binds"] = []any{"excalibase-platform-secrets:/x"}
		}, want: "volume"},
		"a platform volume by mount": {mutate: func(b map[string]any) {
			hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "single-host_postgres_data", "Target": "/x"}}
		}, want: "volume"},
		"an image mount": {mutate: func(b map[string]any) {
			hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "image", "Source": "alpine", "Target": "/x"}}
		}, want: "mount"},
		"a fixed host port": {mutate: func(b map[string]any) {
			hostConfig(b)["PortBindings"] = map[string]any{"5432/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "22"}}}
		}, want: "port"},
		"oom killer exemption": {mutate: func(b map[string]any) { hostConfig(b)["OomScoreAdj"] = -1000 }, want: "OomScoreAdj"},
		"a network alias": {mutate: func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-tenants": map[string]any{"Aliases": []any{"provisioning"}}}}
		}, want: "endpoint"},
		"a static address": {mutate: func(b map[string]any) {
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-tenants": map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "10.0.0.2"}}}}
		}, want: "endpoint"},
		"no host config": {mutate: func(b map[string]any) { delete(b, "HostConfig") }, want: "HostConfig"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			name := tc.name
			if name == "" {
				name = "excalibase-p1-postgres"
			}
			err := testPolicy().CheckCreate(name, createBody(t, tc.mutate))
			if err == nil {
				t.Fatalf("create allowed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestCheckCreateAllowsWhatAHostedAppNeeds(t *testing.T) {
	body := createBody(t, func(b map[string]any) {
		host := hostConfig(b)
		host["Runtime"] = "runsc"
		host["Memory"] = 268435456
		host["NanoCpus"] = 500000000
		host["PidsLimit"] = 512
		host["CapDrop"] = []any{"ALL"}
		host["SecurityOpt"] = []any{"no-new-privileges"}
		host["ReadonlyRootfs"] = true
		host["Binds"] = []any{"excalibase-proj-p1-app-data:/data"}
		host["MemorySwappiness"] = -1
		host["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "excalibase-proj-p1-x", "Target": "/x"},
			map[string]any{"Type": "tmpfs", "Target": "/tmp"},
			map[string]any{"Type": "volume", "Source": "", "Target": "/anonymous"}}
		host["LogConfig"] = map[string]any{"Type": "json-file", "Config": map[string]any{"max-size": "10m"}}
		host["NetworkMode"] = "excalibase-proj-p1"
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": map[string]any{}}}
	})
	if err := testPolicy().CheckCreate("excalibase-p1-app-web", body); err != nil {
		t.Fatalf("app create refused: %v", err)
	}
}

func TestCheckCreateRefusesMalformedJSON(t *testing.T) {
	if err := testPolicy().CheckCreate("excalibase-x", []byte("{")); err == nil {
		t.Fatal("malformed body allowed")
	}
}

func TestCheckExecCreate(t *testing.T) {
	if err := CheckExecCreate([]byte(`{"Cmd":["pg_isready"],"Privileged":false}`)); err != nil {
		t.Fatalf("plain exec refused: %v", err)
	}
	if err := CheckExecCreate([]byte(`{"Cmd":["sh"],"Privileged":true}`)); err == nil {
		t.Fatal("privileged exec allowed")
	}
	if err := CheckExecCreate([]byte(`{`)); err == nil {
		t.Fatal("malformed exec allowed")
	}
}

// The docker CLI fills a few fields the SDK leaves unset.
func TestCheckCreateAllowsWhatTheDockerCLISends(t *testing.T) {
	body := createBody(t, func(b map[string]any) {
		host := hostConfig(b)
		host["MemorySwappiness"] = -1
		host["ConsoleSize"] = []any{0, 0}
		host["RestartPolicy"] = map[string]any{"Name": "no", "MaximumRetryCount": 0}
	})
	if err := testPolicy().CheckCreate("excalibase-p1-postgres", body); err != nil {
		t.Fatalf("CLI create refused: %v", err)
	}
}

// Without a volume prefix no named volume is admitted at all.
func TestCheckCreateRefusesNamedVolumesWithoutAPrefix(t *testing.T) {
	policy := testPolicy()
	policy.VolumePrefix = ""
	body := createBody(t, func(b map[string]any) { hostConfig(b)["Binds"] = []any{"excalibase-proj-p1:/x"} })
	if err := policy.CheckCreate("excalibase-p1-postgres", body); err == nil {
		t.Fatal("named volume admitted with no prefix configured")
	}
}
