package setup

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/fmtx"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/install"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/purge"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/validate"
	"github.com/k2so-dev/awgx/internal/xui"
)

const (
	ClientDir   = "/root/awgx-clients"
	CascadeName = "cascade"
	QUICSNI     = "www.google.com"
)

const (
	Standard = "standard"
	Stealth  = "stealth"
	Speed    = "speed"
)

const (
	EgressNone   = ""
	EgressDirect = "direct"
	EgressKeep   = "keep"
	EgressApply  = "apply"
	EgressUplink = "uplink"
	EgressXUI    = "xui"
)

type Probe struct {
	Installed   bool
	ServeOn     bool
	Results     []doctor.Result
	Legacy      []doctor.LegacyItem
	LocalIP     netip.Addr
	PublicIP    netip.Addr
	PublicErr   string
	Port443Free bool
	XUI         bool
	Instance    *state.Instance
	Clients     int
	EgressDraft string
	EgressLive  string
	Profiles    []string
	HasIPv6     bool
}

func (p Probe) HasProfile(name string) bool {
	return slices.Contains(p.Profiles, name)
}

var ErrContainer = errors.New("this is a container: kernel modules cannot be loaded here, awgx needs a VM or a dedicated server")

func (p Probe) Container() bool {
	for _, r := range p.Results {
		if r.ID == "virt" && r.Status == doctor.Fail {
			return true
		}
	}
	return false
}

func (p Probe) Fixable() []doctor.Result {
	var out []doctor.Result
	for _, r := range p.Results {
		if r.Fix != nil && r.Status != doctor.OK {
			out = append(out, r)
		}
	}
	return out
}

func (p Probe) BehindNAT() bool {
	return p.PublicIP.IsValid() && p.LocalIP.IsValid() && p.PublicIP != p.LocalIP
}

func (p Probe) SuggestedEndpoint() string {
	if p.PublicIP.IsValid() {
		return p.PublicIP.String()
	}
	if p.LocalIP.IsValid() {
		return p.LocalIP.String()
	}
	return ""
}

func (p Probe) EndpointWrong() bool {
	return p.Instance != nil && p.PublicIP.IsValid() && p.Instance.Endpoint != p.PublicIP.String() &&
		p.Instance.Endpoint == p.LocalIP.String()
}

func Inspect(ctx context.Context, app *core.App, lookupPublic bool) Probe {
	p := Probe{Installed: app.Installed(), XUI: xui.Installed()}
	env := app.DoctorEnv()
	p.Results = doctor.Run(ctx, env)
	p.Legacy = doctor.Legacy(ctx, env)
	p.Instance = env.Instance
	if up, err := netx.DefaultUplink(ctx, app.Run); err == nil {
		p.LocalIP = up.Source
	}
	p.HasIPv6 = netx.HasIPv6Route(ctx, app.Run)
	if lookupPublic {
		if a, err := netx.PublicIPv4(ctx); err == nil {
			p.PublicIP = a
		} else {
			p.PublicErr = err.Error()
		}
	} else if p.LocalIP.IsValid() && !p.LocalIP.IsPrivate() {
		p.PublicIP = p.LocalIP
	}
	out, _ := app.Run.Run(ctx, "ss", "-Hulnp", "sport = :443")
	p.Port443Free = strings.TrimSpace(string(out)) == ""
	if p.Installed {
		p.ServeOn = app.ServeEnabled(ctx)
		if rows, err := app.PeerRows(ctx, ""); err == nil {
			p.Clients = len(rows)
		}
		if s, err := app.Store(); err == nil && p.Instance != nil {
			if draft, err := s.Draft(); err == nil {
				p.EgressDraft = profileOf(draft.Default[p.Instance.Name])
				for _, prof := range draft.Profiles {
					p.Profiles = append(p.Profiles, prof.Name)
				}
			}
			if live, err := s.Applied(); err == nil {
				p.EgressLive = profileOf(live.Config.Default[p.Instance.Name])
			}
		}
	}
	return p
}

func routed(profile string) bool {
	return profile != "" && profile != state.Direct
}

func profileOf(a state.Assign) string {
	if a.Profile == "" {
		return state.Direct
	}
	return a.Profile
}

const (
	StepFixes    = "fix problems"
	StepLegacy   = "remove old setups"
	StepInstall  = "install"
	StepEndpoint = "change endpoint"
	StepIPv6     = "ipv6"
	StepClients  = "create clients"
	StepEgress   = "egress"
)

type Plan struct {
	Fixes        []string
	Legacy       []doctor.LegacyItem
	BackupLegacy bool
	Install      bool
	Gen          core.GenOptions
	Endpoint     string
	IPv6         *bool
	Clients      []string
	Egress       string
	UplinkConf   string
	XUIToken     string
	Killswitch   bool
	Serve        bool
	Progress     func(step, total int, name string)
}

func (p Plan) Steps() []string {
	var steps []string
	add := func(on bool, name string) {
		if on {
			steps = append(steps, name)
		}
	}
	add(len(p.Fixes) > 0, StepFixes)
	add(len(p.Legacy) > 0, StepLegacy)
	add(p.Install, StepInstall)
	add(!p.Install && p.Endpoint != "", StepEndpoint)
	add(!p.Install && p.IPv6 != nil, StepIPv6)
	add(len(p.Clients) > 0, StepClients)
	add(p.Egress != EgressNone && p.Egress != EgressKeep, StepEgress)
	return steps
}

func (p Plan) step(name string) {
	if p.Progress == nil {
		return
	}
	steps := p.Steps()
	for i, s := range steps {
		if s == name {
			p.Progress(i+1, len(steps), name)
		}
	}
}

func ScenarioOptions(scenario string, probe Probe) core.GenOptions {
	switch scenario {
	case Stealth:
		if probe.Port443Free {
			return core.GenOptions{Preset: "stealth", Port: 443, I1: i1.Settings{Profile: i1.QUIC, SNI: QUICSNI}}
		}
		return core.GenOptions{Preset: "stealth", I1: i1.Settings{Profile: i1.STUN}}
	case Speed:
		return core.GenOptions{Preset: "speed"}
	default:
		return core.GenOptions{Preset: "balanced"}
	}
}

func Defaults(probe Probe) Plan {
	p := Plan{Install: !probe.Installed, Killswitch: true}
	if probe.Installed && (routed(probe.EgressDraft) || routed(probe.EgressLive)) {
		p.Egress = EgressKeep
	}
	for _, r := range probe.Fixable() {
		p.Fixes = append(p.Fixes, r.ID)
	}
	if p.Install {
		p.Gen = ScenarioOptions(Standard, probe)
		p.Endpoint = probe.SuggestedEndpoint()
		p.Clients = []string{"phone"}
	} else if probe.EndpointWrong() {
		p.Endpoint = probe.PublicIP.String()
	}
	return p
}

func (p Plan) Validate() error {
	if p.Endpoint != "" {
		if err := core.ValidEndpoint(p.Endpoint); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, c := range p.Clients {
		if !core.ValidPeerName(c) {
			return fmt.Errorf("client name %q: use latin letters, digits and -", c)
		}
		if seen[c] {
			return fmt.Errorf("client %s is listed twice", c)
		}
		seen[c] = true
	}
	switch p.Egress {
	case EgressUplink:
		if strings.TrimSpace(p.UplinkConf) == "" {
			return errors.New("paste the client .conf from the other server")
		}
	case EgressXUI:
		if strings.TrimSpace(p.XUIToken) == "" {
			return errors.New("the 3x-ui API token is empty")
		}
	}
	return nil
}

func (p Plan) Summary(probe Probe) []string {
	var out []string
	for _, r := range probe.Results {
		if slices.Contains(p.Fixes, r.ID) && r.Fix != nil {
			out = append(out, "fix: "+r.Fix.Description)
		}
	}
	for _, l := range p.Legacy {
		verb := "remove"
		if p.BackupLegacy {
			verb = "back up and remove"
		}
		out = append(out, fmt.Sprintf("%s %s %s", verb, l.Kind, l.Name))
	}
	if p.Install {
		out = append(out, "install AmneziaWG: kernel module from the Amnezia PPA")
		port := "random port"
		if p.Gen.Port != 0 {
			port = fmt.Sprintf("%d/udp", p.Gen.Port)
		}
		i1p := p.Gen.I1.Profile
		if i1p == "" {
			i1p = i1.None
		}
		out = append(out, fmt.Sprintf("server awg0: preset %s, %s, I1 %s, endpoint %s, IPv6 %s", p.Gen.Preset, port, i1p, orDetect(p.Endpoint), ipv6Text(p.Gen.IPv6, probe.HasIPv6)))
	} else {
		if p.Endpoint != "" {
			out = append(out, "set the endpoint of "+probe.Instance.Name+" to "+p.Endpoint)
		}
		if p.IPv6 != nil {
			out = append(out, "turn IPv6 inside the tunnel of "+probe.Instance.Name+" "+ipv6Text(p.IPv6, false)+", clients import their configs again")
		}
	}
	if len(p.Clients) > 0 {
		out = append(out, "clients "+strings.Join(p.Clients, ", ")+", configs in "+ClientDir)
	}
	kill := ", traffic is dropped while it is down"
	if !p.Killswitch {
		kill = ", traffic goes direct while it is down"
	}
	switch p.Egress {
	case EgressApply:
		out = append(out, "apply the egress draft: clients through "+probe.EgressDraft)
	case EgressDirect:
		from := probe.EgressDraft
		if !routed(from) {
			from = probe.EgressLive
		}
		out = append(out, "clients straight to the internet again instead of "+from)
	case EgressUplink:
		out = append(out, "cascade: all clients through the server from the pasted config"+kill)
	case EgressXUI:
		out = append(out, "all clients through the local 3x-ui (Xray)"+kill)
	}
	if p.Install {
		out = append(out, "background service (stats, client expiry, egress failover, update check): "+fmtx.OnOff(p.Serve))
	} else if p.Serve && !probe.ServeOn {
		out = append(out, "turn on the background service (stats, client expiry, egress failover, update check)")
	}
	return out
}

func orDetect(s string) string {
	if s == "" {
		return "detected"
	}
	return s
}

type Report struct {
	Instance    state.Instance  `json:"instance"`
	ClientFiles []string        `json:"client_files,omitempty"`
	Snapshot    int64           `json:"egress_snapshot,omitempty"`
	Issues      validate.Issues `json:"issues,omitempty"`
	Notes       []string        `json:"notes,omitempty"`
}

func Run(ctx context.Context, app *core.App, p Plan) (Report, error) {
	var rep Report
	if err := p.Validate(); err != nil {
		return rep, err
	}
	if !sysx.IsRoot() {
		return rep, errors.New("setup must run as root")
	}
	p.step(StepFixes)
	applyFixes(ctx, app, p.Fixes, &rep)
	if len(p.Legacy) > 0 {
		p.step(StepLegacy)
		app.Logf("removing old setups")
		if err := purge.Legacy(ctx, app, p.Legacy, p.BackupLegacy); err != nil {
			return rep, err
		}
	}
	if p.Install {
		p.step(StepInstall)
		gen := p.Gen
		gen.Endpoint = p.Endpoint
		res, err := install.Run(ctx, app, install.Options{
			Gen:        gen,
			Serve:      p.Serve,
			SkipLegacy: true,
		})
		rep.Issues = res.Issues
		if err != nil {
			return rep, err
		}
		applyFixes(ctx, app, []string{"firewall"}, &rep)
	}
	inst, err := app.Current()
	if p.Install {
		inst, err = app.ResolveInstance("")
	}
	if err != nil {
		return rep, err
	}
	if !p.Install && p.Endpoint != "" && p.Endpoint != inst.Endpoint {
		p.step(StepEndpoint)
		err := app.Locked(func() (err error) {
			inst, err = app.SetEndpoint(inst.Name, p.Endpoint)
			return err
		})
		if err != nil {
			return rep, err
		}
		rep.Notes = append(rep.Notes, "the endpoint changed: re-export the configs of existing clients")
	}
	if !p.Install && p.IPv6 != nil && *p.IPv6 != inst.IPv6() {
		p.step(StepIPv6)
		if err := app.Locked(func() (err error) {
			inst, err = app.SetIPv6(ctx, inst.Name, *p.IPv6)
			return err
		}); err != nil {
			return rep, err
		}
		rep.Notes = append(rep.Notes, "IPv6 inside the tunnel changed: re-export the configs of existing clients")
	}
	rep.Instance = inst
	if !p.Install && p.Serve && !app.ServeEnabled(ctx) {
		if err := app.StartServe(ctx); err != nil {
			return rep, err
		}
	}
	p.step(StepClients)
	if err := addClients(ctx, app, inst, p.Clients, &rep); err != nil {
		return rep, err
	}
	if p.Egress != EgressNone && p.Egress != EgressKeep {
		p.step(StepEgress)
		if err := setupEgress(ctx, app, inst, p, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func applyFixes(ctx context.Context, app *core.App, ids []string, rep *Report) {
	if len(ids) == 0 {
		return
	}
	rep.Notes = append(rep.Notes, doctor.ApplyFixes(ctx, app.DoctorEnv(), ids, func(l string) { app.Logf("%s", l) })...)
}

func addClients(ctx context.Context, app *core.App, inst state.Instance, names []string, rep *Report) error {
	for _, name := range names {
		var p state.Peer
		err := app.Locked(func() (err error) {
			p, _, err = app.AddPeer(ctx, core.PeerOptions{Name: name, Instance: inst.Name})
			return err
		})
		if err != nil {
			return fmt.Errorf("client %s: %w", name, err)
		}
		files, err := writeClient(inst, p)
		rep.ClientFiles = append(rep.ClientFiles, files...)
		if err != nil {
			return err
		}
		app.Logf("client %s added: %s", name, files[0])
	}
	return nil
}

func writeClient(inst state.Instance, p state.Peer) ([]string, error) {
	if err := os.MkdirAll(ClientDir, 0o700); err != nil {
		return nil, err
	}
	conf := filepath.Join(ClientDir, p.Name+".conf")
	if err := os.WriteFile(conf, []byte(render.Client(inst, p).String()), 0o600); err != nil {
		return nil, err
	}
	files := []string{conf}
	if url, err := render.VPNURL(inst, p); err == nil {
		vpn := filepath.Join(ClientDir, p.Name+".vpn.txt")
		if err := os.WriteFile(vpn, []byte(url+"\n"), 0o600); err != nil {
			return files, err
		}
		files = append(files, vpn)
	}
	return files, nil
}

func setupEgress(ctx context.Context, app *core.App, inst state.Instance, p Plan, rep *Report) error {
	return egress.Locked(app, func(e *egress.Engine) error {
		switch p.Egress {
		case EgressApply:
		case EgressDirect:
			if err := e.Assign(inst.Name, nil, state.Assign{Profile: state.Direct}); err != nil {
				return err
			}
		case EgressUplink:
			draft, err := e.Store.Draft()
			if err != nil {
				return err
			}
			if _, ok := draft.Profile(CascadeName); ok {
				return fmt.Errorf("an egress profile %s exists already, change it with awgx egress", CascadeName)
			}
			name, err := freeUplinkName(app, e)
			if err != nil {
				return err
			}
			app.Logf("bringing up uplink %s", name)
			warnings, err := e.AddUplink(ctx, name, p.UplinkConf, 0, false)
			rep.Notes = append(rep.Notes, warnings...)
			if err != nil {
				return err
			}
			prof := state.EgressProfile{Name: CascadeName, Kind: "iface", Hops: []state.Hop{{Dev: name}}}
			if !p.Killswitch {
				prof.Fallback = state.Direct
			}
			if _, err := e.AddProfile(ctx, prof); err != nil {
				return err
			}
			if err := e.Assign(inst.Name, nil, state.Assign{Profile: CascadeName, Killswitch: p.Killswitch}); err != nil {
				return err
			}
		case EgressXUI:
			o := xui.ConnectOptions{Auth: xui.Auth{Token: strings.TrimSpace(p.XUIToken)}, Instance: inst.Name, Killswitch: p.Killswitch}
			_, notes, err := xui.Connect(ctx, e, o)
			rep.Notes = append(rep.Notes, notes...)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown egress %q", p.Egress)
		}
		app.Logf("applying the egress: checks, snapshot")
		res, converged, err := e.ApplyOrConverge(ctx, egress.ApplyOptions{SSH: sysx.CurrentSSH()})
		if converged && err == nil {
			rep.Notes = append(rep.Notes, "the egress already matches the draft")
			return nil
		}
		rep.Notes = append(rep.Notes, res.Checks...)
		rep.Snapshot = res.Snapshot
		return err
	})
}

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func EgressChoices(p Probe) []Choice {
	var out []Choice
	current := routed(p.EgressDraft)
	direct := "straight to the internet (NAT)"
	switch {
	case !current && routed(p.EgressLive):
		out = append(out, Choice{EgressKeep, "leave as is: through " + p.EgressLive + ", the direct draft is not applied"}, Choice{EgressDirect, direct})
	case current && p.EgressDraft != p.EgressLive:
		out = append(out, Choice{EgressKeep, "leave as is: " + p.EgressDraft + " is configured but not active"},
			Choice{EgressApply, "activate " + p.EgressDraft}, Choice{EgressDirect, direct})
	case current:
		out = append(out, Choice{EgressKeep, "leave as is: through " + p.EgressDraft}, Choice{EgressDirect, direct})
	default:
		out = append(out, Choice{EgressNone, direct})
	}
	if !p.HasProfile(CascadeName) {
		out = append(out, Choice{EgressUplink, "through another AmneziaWG server (cascade)"})
	}
	if p.XUI && !p.HasProfile("xui") {
		out = append(out, Choice{EgressXUI, "through the 3x-ui on this server (Xray)"})
	}
	return out
}

func freeUplinkName(app *core.App, e *egress.Engine) (string, error) {
	insts := app.InstanceNames()
	for n := 1; n < 100; n++ {
		name := fmt.Sprintf("awg%d", n)
		if slices.Contains(insts, name) || app.InterfaceUp(name) || sysx.Exists(app.Paths.Conf(name)) {
			continue
		}
		if _, err := e.Store.Uplink(name); err == nil {
			continue
		}
		return name, nil
	}
	return "", errors.New("no free awgN name for the uplink")
}

func ipv6Text(v *bool, auto bool) string {
	if v != nil {
		auto = *v
	}
	if auto {
		return "on"
	}
	return "off"
}
