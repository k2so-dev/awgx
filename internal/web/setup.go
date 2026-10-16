package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/setup"
)

const probeTTL = 10 * time.Minute

type probeCache struct {
	mu    sync.Mutex
	at    time.Time
	probe *setup.Probe
}

func (s *Server) probe(ctx context.Context, fresh bool) setup.Probe {
	s.setupProbe.mu.Lock()
	defer s.setupProbe.mu.Unlock()
	if fresh || s.setupProbe.probe == nil || time.Since(s.setupProbe.at) > probeTTL {
		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		p := setup.Inspect(ctx, s.setupApp(s.app), true)
		s.setupProbe.probe, s.setupProbe.at = &p, time.Now()
	}
	return *s.setupProbe.probe
}

func (s *Server) setupApp(app *core.App) *core.App {
	a := app.WithOut(app.Out)
	if inst, err := s.defaultInstance(); err == nil {
		a.Selected = inst.Name
	}
	return a
}

func (s *Server) setupInfo(w http.ResponseWriter, r *http.Request) {
	p := s.probe(context.WithoutCancel(r.Context()), r.URL.Query().Has("fresh"))
	if p.Instance == nil {
		writeError(w, http.StatusConflict, errors.New("no instance found"))
		return
	}
	plan := setup.Defaults(p)
	type fix struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Message     string `json:"message"`
		Description string `json:"description"`
	}
	fixes := []fix{}
	for _, f := range p.Fixable() {
		fixes = append(fixes, fix{f.ID, string(f.Status), f.Message, f.Fix.Description})
	}
	var warn, fail int
	for _, res := range p.Results {
		switch res.Status {
		case doctor.Warn:
			warn++
		case doctor.Fail:
			fail++
		}
	}
	legacy := p.Legacy
	if legacy == nil {
		legacy = []doctor.LegacyItem{}
	}
	writeJSON(w, map[string]any{
		"instance": p.Instance.Name, "endpoint": p.Instance.Endpoint, "public_ip": addrOrEmpty(p.PublicIP.String(), p.PublicIP.IsValid()),
		"local_ip":       addrOrEmpty(p.LocalIP.String(), p.LocalIP.IsValid()),
		"endpoint_wrong": p.EndpointWrong(), "behind_nat": p.BehindNAT(), "clients": p.Clients,
		"checks": map[string]int{"total": len(p.Results), "warn": warn, "fail": fail}, "fixes": fixes, "legacy": legacy,
		"egress_draft": p.EgressDraft, "egress_live": p.EgressLive, "egress_choices": setup.EgressChoices(p),
		"default_egress": plan.Egress, "suggested_endpoint": plan.Endpoint, "ipv6": p.Instance.IPv6(), "has_ipv6": p.HasIPv6,
		"public_err": p.PublicErr, "killswitch": plan.Killswitch,
	})
}

func addrOrEmpty(s string, ok bool) string {
	if ok {
		return s
	}
	return ""
}

type setupRequest struct {
	Pick       []string `json:"pick"`
	Fixes      []string `json:"fixes"`
	Legacy     []string `json:"legacy"`
	BackupOld  bool     `json:"backup_old"`
	Endpoint   string   `json:"endpoint"`
	IPv6       *bool    `json:"ipv6"`
	Clients    []string `json:"clients"`
	Egress     string   `json:"egress"`
	UplinkConf string   `json:"uplink_conf"`
	XUIToken   string   `json:"xui_token"`
	Killswitch bool     `json:"killswitch"`
}

func (req setupRequest) plan(p setup.Probe) (setup.Plan, error) {
	chosen := func(id string) bool { return slices.Contains(req.Pick, id) }
	plan := setup.Plan{Egress: setup.EgressKeep, Killswitch: req.Killswitch}
	if p.Instance == nil {
		return plan, errors.New("no instance found")
	}
	if chosen("check") {
		for _, id := range req.Fixes {
			for _, f := range p.Fixable() {
				if f.ID == id {
					plan.Fixes = append(plan.Fixes, id)
				}
			}
		}
		for _, key := range req.Legacy {
			i := slices.IndexFunc(p.Legacy, func(l doctor.LegacyItem) bool { return l.Kind+"/"+l.Name == key })
			if i < 0 {
				return plan, fmt.Errorf("old setup %s is gone: run the check again", key)
			}
			plan.Legacy = append(plan.Legacy, p.Legacy[i])
		}
		plan.BackupLegacy = req.BackupOld && len(plan.Legacy) > 0
	}
	if chosen("endpoint") && strings.TrimSpace(req.Endpoint) != p.Instance.Endpoint {
		plan.Endpoint = strings.TrimSpace(req.Endpoint)
	}
	if chosen("ipv6") && req.IPv6 != nil && *req.IPv6 != p.Instance.IPv6() {
		plan.IPv6 = req.IPv6
	}
	if chosen("clients") {
		for _, c := range req.Clients {
			if c = strings.TrimSpace(c); c != "" {
				plan.Clients = append(plan.Clients, c)
			}
		}
	}
	if chosen("egress") {
		ok := false
		for _, c := range setup.EgressChoices(p) {
			ok = ok || c.Value == req.Egress
		}
		if !ok {
			return plan, errors.New("choose where client traffic goes")
		}
		plan.Egress = req.Egress
		plan.XUIToken = req.XUIToken
		if plan.Egress == setup.EgressUplink {
			plan.UplinkConf = req.UplinkConf
		}
	}
	return plan, plan.Validate()
}

func (s *Server) setupSummary(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p := s.probe(context.WithoutCancel(r.Context()), false)
	plan, err := req.plan(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	lines := plan.Summary(p)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, map[string]any{"summary": lines})
}

func (s *Server) setupRun(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p := s.probe(context.WithoutCancel(r.Context()), false)
	plan, err := req.plan(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	plan.Progress = s.jobs.progress
	s.startJob(w, "setup", func(ctx context.Context, app *core.App) (any, error) {
		rep, err := setup.Run(ctx, s.setupApp(app), plan)
		s.setupProbe.mu.Lock()
		s.setupProbe.probe = nil
		s.setupProbe.mu.Unlock()
		clients := []string{}
		for _, f := range rep.ClientFiles {
			if name, ok := strings.CutSuffix(filepath.Base(f), ".conf"); ok {
				clients = append(clients, name)
			}
		}
		notes := rep.Notes
		for _, i := range rep.Issues {
			notes = append(notes, i.String())
		}
		return map[string]any{"clients": clients, "notes": notes}, err
	})
}
