package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/selftest"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

type instanceDetail struct {
	Name        string      `json:"name"`
	Endpoint    string      `json:"endpoint"`
	Port        int         `json:"port"`
	MTU         int         `json:"mtu"`
	MTUAuto     int         `json:"mtu_auto"`
	MTUFixed    bool        `json:"mtu_fixed"`
	Rate        int         `json:"rate"`
	Subnet4     string      `json:"subnet4"`
	Subnet6     string      `json:"subnet6"`
	IPv6        bool        `json:"ipv6"`
	DNS         []string    `json:"dns"`
	Preset      string      `json:"preset"`
	I1          i1.Settings `json:"i1"`
	I1Packet    string      `json:"i1_packet"`
	I1RotatedAt time.Time   `json:"i1_rotated_at,omitzero"`
	Params      gen.Fields  `json:"params"`
	Extra       [][2]string `json:"extra"`
	HeaderKey   bool        `json:"header_key"`
	Peers       int         `json:"peers"`
	Up          bool        `json:"up"`
}

func inputIssues(err error) validate.Issues {
	field, msg, ok := strings.Cut(err.Error(), ": ")
	if !ok {
		field, msg = "input", err.Error()
	}
	return validate.Issues{{Level: validate.Fail, Field: field, Message: msg}}
}

func detailOf(inst state.Instance, peers int, up bool) instanceDetail {
	o := inst.Obfuscation
	d := instanceDetail{
		Name: inst.Name, Endpoint: inst.Endpoint, Port: inst.ListenPort, MTU: inst.MTU,
		Subnet4: inst.Subnet4.String(), Subnet6: state.OrEmpty(inst.Subnet6), IPv6: inst.IPv6(), DNS: []string{},
		Preset: inst.Preset, I1: inst.I1, I1Packet: o.I[0], I1RotatedAt: inst.I1RotatedAt,
		Params: gen.FieldsOf(o), Extra: [][2]string{}, HeaderKey: !o.HeaderProtectionKey.IsZero(),
		Peers: peers, Up: up,
	}
	for _, a := range inst.DNS {
		d.DNS = append(d.DNS, a.String())
	}
	for _, kv := range []struct {
		name string
		r    awgconf.Range
	}{
		{"ContentPaddingAddition", o.ContentPaddingAddition},
		{"RekeyAfterTime", o.RekeyAfterTime},
		{"RekeyTimeout", o.RekeyTimeout},
		{"RejectAfterTime", o.RejectAfterTime},
		{"KeepaliveTimeout", o.KeepaliveTimeout},
		{"MaxHandshakeAttempts", o.MaxHandshakeAttempts},
	} {
		if !kv.r.IsZero() {
			d.Extra = append(d.Extra, [2]string{kv.name, kv.r.String()})
		}
	}
	return d
}

func (s *Server) instances(w http.ResponseWriter, r *http.Request) {
	insts, err := s.app.Instances()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := []instanceDetail{}
	for _, inst := range insts {
		peers, _ := store.Peers(inst.Name)
		d := detailOf(inst, len(peers), s.app.InterfaceUp(inst.Name))
		d.MTUAuto = s.app.AutoMTU(r.Context(), inst)
		d.MTUFixed = s.app.FixedMTU(inst.Name) > 0
		d.Rate = s.app.Rate(inst.Name)
		out = append(out, d)
	}
	writeJSON(w, out)
}

func (s *Server) presets(w http.ResponseWriter, r *http.Request) {
	base, err := gen.Obfuscation(gen.Presets[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"presets": gen.PresetNames(), "custom": gen.FieldsOf(base), "has_ipv6": netx.HasIPv6Route(r.Context(), s.app.Run)})
}

func writeIssues(w http.ResponseWriter, err error, is validate.Issues) {
	if is == nil {
		is = validate.Issues{}
	}
	writeStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "issues": is})
}

type obfRequest struct {
	Preset string     `json:"preset"`
	Params gen.Fields `json:"params"`
}

func (o obfRequest) custom() (*awgconf.Obfuscation, validate.Issues, error) {
	if o.Preset != gen.Custom {
		return nil, nil, nil
	}
	base, err := gen.Obfuscation(gen.Presets[0])
	if err != nil {
		return nil, nil, err
	}
	obf, err := o.Params.Apply(base)
	if err != nil {
		return nil, inputIssues(err), err
	}
	is := core.CheckCustom(obf)
	return &obf, is, is.Err()
}

func (s *Server) addInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		obfRequest
		Name     string   `json:"name"`
		Port     int      `json:"port"`
		Endpoint string   `json:"endpoint"`
		DNS      []string `json:"dns"`
		I1       string   `json:"i1"`
		SNI      string   `json:"sni"`
		CPS      string   `json:"cps"`
		IPv6     *bool    `json:"ipv6"`
		SelfTest bool     `json:"selftest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := core.ValidInstanceName(req.Name); err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "name"})
		return
	}
	if req.Port != 0 && (req.Port < 1 || req.Port > 65535) {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": "port must be 1-65535", "field": "port"})
		return
	}
	if req.Endpoint != "" {
		if err := core.ValidEndpoint(req.Endpoint); err != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "endpoint"})
			return
		}
	}
	o := core.GenOptions{Name: req.Name, Preset: req.Preset, Port: req.Port, Endpoint: req.Endpoint, IPv6: req.IPv6}
	if o.Preset == "" {
		o.Preset = "balanced"
	}
	for _, d := range req.DNS {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		a, err := netip.ParseAddr(d)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("DNS %q is not an IP address", d), "field": "dns"})
			return
		}
		o.DNS = append(o.DNS, a)
	}
	st, err := i1Settings(req.I1, req.SNI, req.CPS)
	if err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "i1"})
		return
	}
	if st != nil {
		o.I1 = *st
	}
	custom, is, err := req.custom()
	if err != nil {
		writeIssues(w, err, is)
		return
	}
	o.Custom = custom
	s.startJob(w, "instance.add", func(ctx context.Context, app *core.App) (any, error) {
		s.jobs.progress(1, 2, "starting "+o.Name)
		var inst state.Instance
		err := app.Locked(func() (err error) {
			inst, _, err = app.AddInstance(ctx, o)
			return err
		})
		if err != nil {
			return nil, err
		}
		if req.SelfTest {
			s.jobs.progress(2, 2, "self-test")
			app.Logf("self-test: handshake from a test peer in a network namespace")
			if err := selftest.Run(ctx, app, inst); err != nil {
				return nil, fmt.Errorf("self-test failed: %w (remove the instance in Instances)", err)
			}
		}
		return nil, nil
	})
}

func (s *Server) removeInstance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	readJSON(r, &req)
	name := r.PathValue("name")
	if req.Confirm != name {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": "type the instance name to confirm", "field": "confirm"})
		return
	}
	if err := s.app.Locked(func() error { return s.app.RemoveInstance(context.WithoutCancel(r.Context()), name) }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{})
}

func (s *Server) setIPv6(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On *bool `json:"on"`
	}
	if err := readJSON(r, &req); err != nil || req.On == nil {
		writeError(w, http.StatusBadRequest, errors.New("on must be true or false"))
		return
	}
	var inst state.Instance
	err := s.app.Locked(func() (err error) {
		inst, err = s.app.SetIPv6(context.WithoutCancel(r.Context()), r.PathValue("name"), *req.On)
		return err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ipv6": inst.IPv6()})
}

func (s *Server) setEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := s.app.Locked(func() error {
		_, err := s.app.SetEndpoint(r.PathValue("name"), strings.TrimSpace(req.Endpoint))
		return err
	})
	if err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "endpoint"})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) setMTU(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MTU int `json:"mtu"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	err := s.app.Locked(func() error {
		_, err := s.app.SetMTU(context.WithoutCancel(r.Context()), r.PathValue("name"), req.MTU)
		return err
	})
	if err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "mtu"})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) setRate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rate int `json:"rate"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.app.Locked(func() error { return s.app.SetRate(context.WithoutCancel(r.Context()), r.PathValue("name"), req.Rate) }); err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "rate"})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) setInstanceI1(w http.ResponseWriter, r *http.Request) {
	var req struct {
		I1  string `json:"i1"`
		SNI string `json:"sni"`
		CPS string `json:"cps"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	st, err := i1Settings(req.I1, req.SNI, req.CPS)
	if err == nil && st == nil {
		err = errors.New("choose an I1 profile")
	}
	if err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "i1"})
		return
	}
	var is validate.Issues
	err = s.app.Locked(func() error {
		var err error
		_, is, err = s.app.SetInstanceI1(r.PathValue("name"), *st)
		return err
	})
	if err != nil {
		writeIssues(w, err, is)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) rotateI1(w http.ResponseWriter, r *http.Request) {
	var affected []string
	err := s.app.Locked(func() error {
		var err error
		affected, err = s.app.RotateI1(r.PathValue("name"))
		return err
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if affected == nil {
		affected = []string{}
	}
	writeJSON(w, map[string]any{"affected": affected})
}

func (s *Server) setObfuscation(w http.ResponseWriter, r *http.Request) {
	var req obfRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	if req.Preset != gen.Custom {
		s.applyPreset(w, r, name, req.Preset)
		return
	}
	var is validate.Issues
	err := s.app.Locked(func() error {
		cur, err := s.app.Instance(name)
		if err != nil {
			return err
		}
		obf, err := req.Params.Apply(cur.Obfuscation)
		if err != nil {
			is = inputIssues(err)
			return err
		}
		if obf == cur.Obfuscation {
			return nil
		}
		_, is, err = s.app.SetObfuscation(context.WithoutCancel(r.Context()), name, obf)
		return err
	})
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeIssues(w, err, is)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "issues": is})
}

func (s *Server) applyPreset(w http.ResponseWriter, r *http.Request, name, preset string) {
	var is validate.Issues
	err := s.app.Locked(func() error {
		cur, err := s.app.Instance(name)
		if err != nil {
			return err
		}
		next, issues, err := s.app.Regenerate(cur, preset, gen.Toggles{})
		is = issues
		if err != nil {
			return err
		}
		return s.app.SaveRegenerated(context.WithoutCancel(r.Context()), next)
	})
	if err != nil {
		writeIssues(w, err, is)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "issues": is})
}
