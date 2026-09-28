package apphost_test

import (
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func tcp(port int) apphost.InternalPort { return apphost.InternalPort{Port: port, Protocol: "TCP"} }

// Internal ports are raw TCP ports the project's own apps reach (EXC-525);
// every one is checked, nothing is defaulted.
func TestValidateInternalPorts(t *testing.T) {
	refused := map[string][]apphost.InternalPort{
		"a UDP port":              {{Port: 5353, Protocol: "UDP"}},
		"no protocol":             {{Port: 6379}},
		"lower-case protocol":     {{Port: 6379, Protocol: "tcp"}},
		"below the range":         {tcp(1023)},
		"the Service's HTTP port": {tcp(80)},
		"above the range":         {tcp(65536)},
		"the app's HTTP port":     {tcp(8080)},
		"a duplicate":             {tcp(6379), tcp(6379)},
		"too many":                {tcp(2001), tcp(2002), tcp(2003), tcp(2004), tcp(2005), tcp(2006), tcp(2007), tcp(2008), tcp(2009)},
	}
	for name, ports := range refused {
		app := validApp()
		app.InternalPorts = ports
		if err := app.Validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	app := validApp()
	app.InternalPorts = []apphost.InternalPort{tcp(6379), tcp(1024), tcp(65535)}
	if err := app.Validate(); err != nil {
		t.Fatalf("valid internal ports refused: %v", err)
	}
}

func TestDeployConfigCarriesTheInternalPorts(t *testing.T) {
	app := validApp()
	app.InternalPorts = []apphost.InternalPort{tcp(6379)}
	cfg := apphost.ConfigFromApp(app)
	app.InternalPorts[0].Port = 9999
	if !reflect.DeepEqual(cfg.InternalPorts, []apphost.InternalPort{tcp(6379)}) {
		t.Fatalf("the frozen config must not follow a later edit: %+v", cfg.InternalPorts)
	}
	if got := cfg.ToApp(app.ID, app.ProjectID, app.Name).InternalPorts; !reflect.DeepEqual(got, cfg.InternalPorts) {
		t.Fatalf("ToApp lost the internal ports: %+v", got)
	}
}
