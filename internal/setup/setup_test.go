package setup

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
)

func TestScenarioOptions(t *testing.T) {
	o := ScenarioOptions(Stealth, Probe{Port443Free: true})
	if o.Preset != "stealth" || o.Port != 443 || o.I1.Profile != i1.QUIC || o.I1.SNI == "" {
		t.Fatalf("stealth with free 443: %+v", o)
	}
	o = ScenarioOptions(Stealth, Probe{})
	if o.Port != 0 || o.I1.Profile != i1.STUN {
		t.Fatalf("stealth with busy 443: %+v", o)
	}
	if o := ScenarioOptions(Speed, Probe{}); o.Preset != "speed" {
		t.Fatalf("speed: %+v", o)
	}
}

func TestDefaults(t *testing.T) {
	fix := &doctor.Fix{Description: "write forward"}
	probe := Probe{
		LocalIP:  netip.MustParseAddr("10.0.0.5"),
		PublicIP: netip.MustParseAddr("203.0.113.7"),
		Results: []doctor.Result{
			{ID: "net.forward", Status: doctor.Fail, Fix: fix},
			{ID: "buffers", Status: doctor.OK, Fix: fix},
			{ID: "virt", Group: "virtualization", Status: doctor.Fail},
		},
	}
	p := Defaults(probe)
	if !p.Install || p.Endpoint != "203.0.113.7" || len(p.Clients) != 1 {
		t.Fatalf("fresh defaults: %+v", p)
	}
	if len(p.Fixes) != 1 || p.Fixes[0] != "net.forward" {
		t.Fatalf("fixes: %v", p.Fixes)
	}
	if !probe.BehindNAT() {
		t.Fatal("NAT not detected")
	}

	inst := state.Instance{Name: "awg0", Endpoint: "10.0.0.5"}
	probe = Probe{Installed: true, Instance: &inst, LocalIP: probe.LocalIP, PublicIP: probe.PublicIP}
	p = Defaults(probe)
	if p.Install || p.Endpoint != "203.0.113.7" || len(p.Clients) != 0 {
		t.Fatalf("maintenance with a private endpoint: %+v", p)
	}
	if s := strings.Join(p.Summary(probe), "\n"); strings.Contains(s, "update check") {
		t.Fatalf("maintenance touches the update check:\n%s", s)
	}
	inst.Endpoint = "vpn.example.com"
	if p = Defaults(probe); p.Endpoint != "" {
		t.Fatalf("a chosen domain endpoint was replaced: %+v", p)
	}
}

func TestValidate(t *testing.T) {
	for _, p := range []Plan{
		{Clients: []string{"ok", "ok"}},
		{Clients: []string{"bad name"}},
		{Endpoint: "127.0.0.1"},
		{Egress: EgressUplink, UplinkConf: " \n"},
		{Egress: EgressXUI},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	if err := (Plan{Clients: []string{"phone"}, Endpoint: "vpn.example.com"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSummary(t *testing.T) {
	probe := Probe{Results: []doctor.Result{{ID: "net.forward", Status: doctor.Fail, Fix: &doctor.Fix{Description: "write forward"}}}}
	p := Plan{Install: true, Fixes: []string{"net.forward"}, Gen: ScenarioOptions(Standard, probe),
		Clients: []string{"phone"}, Serve: true, Egress: EgressXUI, Killswitch: true}
	got := strings.Join(p.Summary(probe), "\n")
	for _, want := range []string{"fix: write forward", "kernel module", "preset balanced", "clients phone", "3x-ui", "dropped", "update check): on"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary misses %q:\n%s", want, got)
		}
	}
}

func TestPlanSteps(t *testing.T) {
	p := Plan{Install: true, Clients: []string{"a"}, Egress: EgressUplink}
	var got []string
	p.Progress = func(step, total int, name string) {
		if total != 3 {
			t.Errorf("total %d", total)
		}
		got = append(got, name)
	}
	for _, s := range p.Steps() {
		p.step(s)
	}
	want := []string{StepInstall, StepClients, StepEgress}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v", got)
	}
}
