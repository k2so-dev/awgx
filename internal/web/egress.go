package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/xui"
)

func (s *Server) egress(w http.ResponseWriter, r *http.Request) {
	e, err := egress.New(s.app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	draft, err := e.Store.Draft()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	applied, err := e.Store.Applied()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	plan := egress.Plan{Applied: applied.ID, Changed: !egress.SameConfig(draft, applied.Config)}
	st := egress.Status{Applied: applied.ID, Health: e.Health()}
	uplinks := []egress.UplinkInfo{}
	if r.URL.Query().Get("brief") == "" {
		if err := e.PinLinks(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if plan, st, err = e.PlanStatus(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if u, _ := e.Uplinks(r.Context()); u != nil {
			uplinks = u
		}
	}
	type profile struct {
		Name      string   `json:"name"`
		Kind      string   `json:"kind"`
		Target    string   `json:"target"`
		Hops      []string `json:"hops"`
		Socks     string   `json:"socks,omitempty"`
		User      string   `json:"user,omitempty"`
		NoUDP     bool     `json:"no_udp"`
		DNSHijack bool     `json:"dns_hijack"`
		Fallback  string   `json:"fallback"`
		Health    string   `json:"health"`
	}
	profiles := []profile{}
	for _, p := range draft.Profiles {
		v := profile{Name: p.Name, Kind: p.Kind, Hops: []string{}, DNSHijack: p.DNSHijack, Fallback: p.Fallback}
		v.Health, _ = egress.ProfileHealth(applied.Config, st.Health, p.Name)
		v.Target = p.Target()
		if p.Socks != nil {
			v.Socks, v.User, v.NoUDP = p.Socks.Address, p.Socks.User, p.Socks.NoUDP
		}
		for _, hop := range p.Hops {
			v.Hops = append(v.Hops, hop.Dev)
		}
		profiles = append(profiles, v)
	}
	if plan.Diff == nil {
		plan.Diff = []string{}
	}
	writeJSON(w, map[string]any{"plan": plan, "status": st, "profiles": profiles, "default": draft.Default, "peers": draft.Peers, "uplinks": uplinks})
}

type check struct {
	Status  string `json:"status"`
	Profile string `json:"profile,omitempty"`
	Message string `json:"message"`
}

func parseChecks(lines []string) []check {
	out := []check{}
	for _, l := range lines {
		status, rest, _ := strings.Cut(l, " ")
		c := check{Status: strings.ToLower(status), Message: strings.TrimSpace(rest)}
		if name, msg, ok := strings.Cut(c.Message, ": "); ok && !strings.Contains(name, " ") {
			c.Profile, c.Message = name, msg
		}
		out = append(out, c)
	}
	return out
}

func (s *Server) egressApply(w http.ResponseWriter, r *http.Request) {
	var res egress.Result
	converged := false
	err := egress.Locked(s.app, func(e *egress.Engine) error {
		var err error
		res, converged, err = e.ApplyOrConverge(context.WithoutCancel(r.Context()), egress.ApplyOptions{})
		return err
	})
	writeApply(w, res, converged, err)
}

func writeApply(w http.ResponseWriter, res egress.Result, converged bool, err error) {
	out := map[string]any{"checks": parseChecks(res.Checks), "converged": converged, "snapshot": res.Snapshot, "base": res.Base, "warning": ""}
	switch {
	case errors.Is(err, egress.ErrPostCheck):
		out["warning"] = "The change is applied, but a post-check failed: " + strings.TrimPrefix(err.Error(), egress.ErrPostCheck.Error()+": ")
	case err != nil:
		code := http.StatusUnprocessableEntity
		if errors.Is(err, egress.ErrInvalid) {
			code = http.StatusBadRequest
		}
		out["error"] = err.Error()
		writeStatus(w, code, out)
		return
	}
	writeJSON(w, out)
}

func (s *Server) egressReset(w http.ResponseWriter, r *http.Request) {
	if err := egress.Locked(s.app, func(e *egress.Engine) error { return e.ResetDraft() }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type profileRequest struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Hops      []string `json:"hops"`
	Socks     string   `json:"socks"`
	User      string   `json:"user"`
	Password  string   `json:"password"`
	NoUDP     bool     `json:"no_udp"`
	DNSHijack *bool    `json:"dns_hijack"`
	Fallback  string   `json:"fallback"`
}

func assignOf(profile string, killswitch bool) state.Assign {
	if profile == "" || profile == "inherit" {
		return state.Assign{}
	}
	return state.Assign{Profile: profile, Killswitch: killswitch}
}

func (p profileRequest) fill(q *state.EgressProfile) error {
	fallback := p.Fallback
	if fallback == "drop" {
		fallback = ""
	}
	if p.Kind == "xui" {
		if q.Kind != "xui" {
			return errors.New("only the 3x-ui profile has type xui")
		}
		q.Fallback = fallback
		if p.DNSHijack != nil {
			q.DNSHijack = *p.DNSHijack
		}
		return nil
	}
	if err := egress.Retarget(q, p.Kind, fallback, p.Hops, state.Socks{Address: p.Socks, User: p.User, Password: p.Password, NoUDP: p.NoUDP}); err != nil {
		return err
	}
	if p.DNSHijack != nil {
		q.DNSHijack = *p.DNSHijack
	}
	return nil
}

func (s *Server) saveProfile(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req profileRequest
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if edit {
			req.Name = r.PathValue("name")
		}
		if req.Kind != "iface" && req.Kind != "socks5" && !(edit && req.Kind == "xui") {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": "type must be iface or socks5", "field": "kind"})
			return
		}
		if req.Kind == "socks5" && strings.TrimSpace(req.Socks) == "" || req.Kind == "iface" && len(req.Hops) == 0 {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": "required", "field": "target"})
			return
		}
		if !edit {
			if err := egress.ValidName(req.Name); err != nil {
				writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "name"})
				return
			}
		}
		ctx := context.WithoutCancel(r.Context())
		err := egress.Locked(s.app, func(e *egress.Engine) error {
			if edit {
				_, err := e.UpdateProfile(ctx, req.Name, req.fill)
				return err
			}
			q := state.EgressProfile{Name: req.Name}
			if err := req.fill(&q); err != nil {
				return err
			}
			_, err := e.AddProfile(ctx, q)
			return err
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *Server) removeProfile(w http.ResponseWriter, r *http.Request) {
	if err := egress.Locked(s.app, func(e *egress.Engine) error { return e.RemoveProfile(r.PathValue("name")) }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) assign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instance   string   `json:"instance"`
		Peers      []string `json:"peers"`
		Profile    string   `json:"profile"`
		Killswitch bool     `json:"killswitch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a := assignOf(req.Profile, req.Killswitch)
	if err := egress.Locked(s.app, func(e *egress.Engine) error { return e.Assign(req.Instance, req.Peers, a) }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) rollbackTo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var res egress.Result
	err = egress.Locked(s.app, func(e *egress.Engine) error {
		var err error
		res, err = e.RollbackTo(context.WithoutCancel(r.Context()), id, egress.ApplyOptions{})
		return err
	})
	writeApply(w, res, false, err)
}

func (s *Server) addUplink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Conf   string `json:"conf"`
		Port   int    `json:"port"`
		Routed bool   `json:"routed"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var warnings []string
	err := egress.Locked(s.app, func(e *egress.Engine) error {
		var err error
		warnings, err = e.AddUplink(context.WithoutCancel(r.Context()), strings.TrimSpace(req.Name), req.Conf, req.Port, req.Routed)
		return err
	})
	if warnings == nil {
		warnings = []string{}
	}
	if err != nil {
		field := ""
		if strings.HasPrefix(err.Error(), "listen port") {
			field = "port"
		}
		writeStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "field": field, "warnings": warnings})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "warnings": warnings})
}

func (s *Server) uplinkConf(w http.ResponseWriter, r *http.Request) {
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	u, err := store.Uplink(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"name": u.Name, "conf": u.Conf, "routed": u.Routed})
}

func (s *Server) updateUplink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Conf   string `json:"conf"`
		MTU    int    `json:"mtu"`
		Port   *int   `json:"port"`
		Routed *bool  `json:"routed"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var warnings []string
	err := egress.Locked(s.app, func(e *egress.Engine) error {
		var err error
		warnings, err = e.UpdateUplink(context.WithoutCancel(r.Context()), r.PathValue("name"),
			egress.UplinkEdit{Conf: strings.TrimSpace(req.Conf), MTU: req.MTU, Port: req.Port, Routed: req.Routed})
		return err
	})
	if warnings == nil {
		warnings = []string{}
	}
	if err != nil {
		field := "conf"
		switch {
		case strings.HasPrefix(err.Error(), "MTU"):
			field = "mtu"
		case strings.HasPrefix(err.Error(), "rate"):
			field = "rate"
		case strings.HasPrefix(err.Error(), "listen port"):
			field = "port"
		}
		writeStatus(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "field": field, "warnings": warnings})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "warnings": warnings})
}

func (s *Server) removeUplink(w http.ResponseWriter, r *http.Request) {
	if err := egress.Locked(s.app, func(e *egress.Engine) error {
		return e.RemoveUplink(context.WithoutCancel(r.Context()), r.PathValue("name"))
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) testUplink(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	var lines []string
	err := egress.Locked(s.app, func(e *egress.Engine) error {
		var err error
		lines, err = e.TestUplink(ctx, r.PathValue("name"))
		return err
	})
	if lines == nil {
		lines = []string{}
	}
	out := map[string]any{"checks": parseChecks(lines)}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

func (s *Server) speedUplink(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.startJob(w, "uplink.speed", func(ctx context.Context, app *core.App) (any, error) {
		return nil, egress.Locked(app, func(e *egress.Engine) error {
			return e.SpeedUplink(ctx, name, egress.SpeedOpts{Seconds: 10})
		})
	})
}

func (s *Server) xuiStatus(w http.ResponseWriter, r *http.Request) {
	e, err := egress.New(s.app)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st, err := xui.Check(ctx, e)
	_, saved, _ := xui.Load(e.Store)
	out := map[string]any{"installed": xui.Installed(), "status": st, "saved_auth": saved}
	if inst, err := s.defaultInstance(); err == nil {
		out["instance"] = inst.Name
	}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, out)
}

func (s *Server) defaultInstance() (state.Instance, error) {
	inst, err := s.app.Current()
	if err == nil {
		return inst, nil
	}
	insts, ierr := s.app.Instances()
	if ierr != nil || len(insts) == 0 {
		return inst, err
	}
	return insts[0], nil
}

func (s *Server) xuiConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instance      string `json:"instance"`
		Token         string `json:"token"`
		FallbackToken bool   `json:"fallback_token"`
		Killswitch    bool   `json:"killswitch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	o := xui.ConnectOptions{Instance: req.Instance, Killswitch: req.Killswitch, Auth: xui.Auth{Token: strings.TrimSpace(req.Token)}}
	s.startJob(w, "xui.connect", func(ctx context.Context, app *core.App) (any, error) {
		store, err := app.Store()
		if err != nil {
			return nil, err
		}
		_, saved, _ := xui.Load(store)
		if req.FallbackToken {
			t, err := xui.FallbackToken(ctx, app.Run)
			if err != nil {
				return nil, err
			}
			o.Auth.Token = t
		} else if o.Auth.Token == "" && !saved {
			return nil, errors.New("give an API token (3x-ui Settings, Security, API Token) or allow the fallback token")
		}
		if o.Instance == "" {
			inst, err := s.defaultInstance()
			if err != nil {
				return nil, err
			}
			o.Instance = inst.Name
		}
		s.jobs.progress(1, 2, "creating the inbound")
		var notes []string
		err = egress.Locked(app, func(e *egress.Engine) error {
			var err error
			_, notes, err = xui.Connect(ctx, e, o)
			return err
		})
		for _, n := range notes {
			app.Logf("%s", n)
		}
		if err != nil {
			return nil, err
		}
		s.jobs.progress(2, 2, "applying egress")
		return s.applyJob(ctx, app)
	})
}

func (s *Server) applyJob(ctx context.Context, app *core.App) (any, error) {
	var res egress.Result
	converged := false
	err := egress.Locked(app, func(e *egress.Engine) error {
		var err error
		res, converged, err = e.ApplyOrConverge(ctx, egress.ApplyOptions{})
		return err
	})
	for _, c := range res.Checks {
		app.Logf("%s", c)
	}
	if converged && err == nil {
		app.Logf("the egress already matches, nothing to apply")
	}
	return nil, err
}

func (s *Server) xuiDisconnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepInbound bool `json:"keep_inbound"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	s.startJob(w, "xui.disconnect", func(ctx context.Context, app *core.App) (any, error) {
		var detached bool
		if err := egress.Locked(app, func(e *egress.Engine) error {
			var err error
			detached, err = xui.Disconnect(e, req.KeepInbound)
			return err
		}); err != nil {
			return nil, err
		}
		e, err := egress.New(app)
		if err != nil {
			return nil, err
		}
		applied, err := e.Store.Applied()
		if err != nil {
			return nil, err
		}
		if _, ok := applied.Config.Profile(xui.ProfileName); ok || detached {
			app.Logf("moving peers back to direct, then disconnecting 3x-ui")
			return s.applyJob(ctx, app)
		}
		err = egress.Locked(app, func(e *egress.Engine) error { return xui.AfterApply(ctx, e) })
		return nil, err
	})
}
