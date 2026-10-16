package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/state"
)

type instanceView struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Port     int    `json:"port"`
	Preset   string `json:"preset"`
	I1       string `json:"i1"`
	Up       bool   `json:"up"`
	Peers    int    `json:"peers"`
	Online   int    `json:"online"`
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	insts, err := s.app.Instances()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rows, err := s.app.PeerRows(r.Context(), "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]instanceView, len(insts))
	index := map[string]int{}
	for i, inst := range insts {
		views[i] = instanceView{Name: inst.Name, Endpoint: inst.Endpoint, Port: inst.ListenPort, Preset: inst.Preset,
			I1: inst.I1.WithDefaults().Profile, Up: s.app.InterfaceUp(inst.Name)}
		index[inst.Name] = i
	}
	var online int
	var rx, tx uint64
	for _, p := range rows {
		i, ok := index[p.Instance]
		if ok {
			views[i].Peers++
		}
		if p.Online {
			online++
			if ok {
				views[i].Online++
			}
		}
		rx += p.RxBytes
		tx += p.TxBytes
	}
	store, _ := s.app.Store()
	update, _ := store.Setting(state.SettingUpdate)
	host, _ := os.Hostname()
	out := map[string]any{
		"instances": views, "peers": len(rows), "online": online, "rx": rx, "tx": tx, "update": update,
		"server": map[string]any{"hostname": host, "version": s.app.Version, "started_at": s.started.UTC()},
	}
	if r.URL.Query().Get("rows") == "1" {
		if rows == nil {
			rows = []core.PeerRow{}
		}
		out["rows"] = rows
	}
	writeJSON(w, out)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	period, err := time.ParseDuration(r.URL.Query().Get("since"))
	if err != nil || period < time.Minute || period > state.Retention {
		period = 24 * time.Hour
	}
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var peerID int64
	if name := r.URL.Query().Get("peer"); name != "" {
		p, err := store.Peer(name)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		peerID = p.ID
	}
	now := time.Now()
	since := now.Add(-period)
	series, err := store.TrafficSeries(state.TrafficFilter{Peer: peerID}, since, now, points)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]any{"series": series}
	if r.URL.Query().Get("brief") == "" {
		if out["peers"], err = store.TrafficTotals(state.TrafficFilter{}, since); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, out)
}

const points = 60

func (s *Server) doctor(rerun bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.docMu.Lock()
		run := rerun || s.docRes == nil
		if !run && r.URL.Query().Has("refresh") && s.docWait == nil && time.Since(s.docAt) > docTTL {
			go s.runDoctor(context.Background(), s.app, false)
		}
		s.docMu.Unlock()
		if run {
			s.runDoctor(context.WithoutCancel(r.Context()), s.app, rerun)
		}
		s.docMu.Lock()
		out := map[string]any{"ran_at": s.docAt, "checks": s.docRes}
		s.docMu.Unlock()
		writeJSON(w, out)
	}
}

const docTTL = 5 * time.Minute

func (s *Server) runDoctor(ctx context.Context, app *core.App, fresh bool) {
	s.docMu.Lock()
	for s.docWait != nil {
		ch := s.docWait
		s.docMu.Unlock()
		<-ch
		if !fresh {
			return
		}
		s.docMu.Lock()
	}
	ch := make(chan struct{})
	s.docWait = ch
	s.docMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	res := doctor.Run(ctx, app.DoctorEnv())
	cancel()
	s.docMu.Lock()
	s.docRes, s.docAt, s.docWait = res, time.Now().UTC(), nil
	s.docMu.Unlock()
	close(ch)
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request) {
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	entries, err := store.Journal(100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []state.JournalEntry{}
	}
	writeJSON(w, entries)
}

func (s *Server) doctorFix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil || len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("choose at least one fix"))
		return
	}
	s.startJob(w, "doctor.fix", func(ctx context.Context, app *core.App) (any, error) {
		env := app.DoctorEnv()
		s.jobs.progress(1, 2, "applying fixes")
		failed := doctor.ApplyFixes(ctx, env, req.IDs, func(l string) { app.Logf("%s", l) })
		s.jobs.progress(2, 2, "checking again")
		s.runDoctor(ctx, app, true)
		if len(failed) > 0 {
			return nil, errors.New(strings.Join(failed, "; "))
		}
		return nil, nil
	})
}
