package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/qr"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
)

func (s *Server) addPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Instance string `json:"instance"`
		Days     int    `json:"days"`
		I1       string `json:"i1"`
		Note     string `json:"note"`
		SNI      string `json:"sni"`
		CPS      string `json:"cps"`
		Egress   string `json:"egress"`
		Net      string `json:"net"`
	}
	err := readJSON(r, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Days < 0 || req.Days > core.MaxDays {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("expiry must be 1-%d days, or empty for never", core.MaxDays), "field": "days"})
		return
	}
	o := core.PeerOptions{Name: req.Name, Instance: req.Instance, Note: req.Note, Egress: req.Egress, NetLabel: strings.TrimSpace(req.Net)}
	if req.Days > 0 {
		o.Expires = time.Duration(req.Days) * 24 * time.Hour
	}
	if o.I1, err = i1Settings(req.I1, req.SNI, req.CPS); err != nil {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "i1"})
		return
	}
	var p state.Peer
	var is validate.Issues
	err = s.app.Locked(func() error {
		var err error
		p, is, err = s.app.AddPeer(context.WithoutCancel(r.Context()), o)
		return err
	})
	switch {
	case errors.Is(err, core.ErrPeerExists):
		writeStatus(w, http.StatusConflict, map[string]string{"error": err.Error(), "field": "name"})
		return
	case errors.Is(err, core.ErrPeerName):
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "name"})
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("web: peer added", "peer", p.Name)
	writeJSON(w, peerWithIssues{p, is})
}

type peerWithIssues struct {
	state.Peer
	Issues validate.Issues `json:"issues,omitempty"`
}

func (s *Server) removePeer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.app.Locked(func() error { return s.app.RemovePeer(context.WithoutCancel(r.Context()), name) }); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("web: peer removed", "peer", name)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) enablePeer(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		err := s.app.Locked(func() error {
			_, err := s.app.UpdatePeer(context.WithoutCancel(r.Context()), name, func(p *state.Peer) error { p.Enabled = on; return nil })
			return err
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *Server) exportPeer(w http.ResponseWriter, r *http.Request) {
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	p, err := store.Peer(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	inst, err := store.Instance(p.Instance)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	exp, err := render.Export(inst, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	payloads, err := render.QRPayloads(inst, p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var qrs []string
	for _, data := range payloads {
		png, err := qr.PNG(data, 384)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		qrs = append(qrs, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png))
	}
	s.log.Info("web: peer exported", "peer", p.Name)
	writeJSON(w, map[string]any{"name": p.Name, "conf": exp.Config, "vpn": exp.VPN, "qr": qrs})
}

func i1Settings(profile, sni, custom string) (*i1.Settings, error) {
	if profile == "" || profile == "inherit" {
		return nil, nil
	}
	if !i1.Valid(profile) {
		return nil, fmt.Errorf("unknown I1 profile %q", profile)
	}
	st := i1.Settings{Profile: profile, SNI: strings.TrimSpace(sni), Custom: strings.TrimSpace(custom)}
	if profile == i1.Custom && st.Custom == "" {
		return nil, errors.New("custom needs a CPS string")
	}
	return &st, nil
}

func (s *Server) editPeer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note      *string `json:"note"`
		Net       *string `json:"net"`
		ExpiresIn *string `json:"expires_in"`
		I1        *string `json:"i1"`
		SNI       string  `json:"sni"`
		CPS       string  `json:"cps"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	var expires time.Duration
	if req.ExpiresIn != nil && *req.ExpiresIn != "never" {
		d, err := core.ParseDuration(strings.TrimSpace(*req.ExpiresIn))
		if err == nil && d == 0 {
			err = errors.New("use a duration like 30d or 12h, or never")
		}
		if err != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "expires_in"})
			return
		}
		expires = d
	}
	var settings *i1.Settings
	if req.I1 != nil {
		st, err := i1Settings(*req.I1, req.SNI, req.CPS)
		if err != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "i1"})
			return
		}
		settings = st
	}
	var p state.Peer
	var is validate.Issues
	err := s.app.Locked(func() error {
		var err error
		p, err = s.app.UpdatePeer(context.WithoutCancel(r.Context()), name, func(p *state.Peer) error {
			if req.Note != nil {
				p.Note = strings.TrimSpace(*req.Note)
			}
			if req.Net != nil {
				p.NetLabel = strings.TrimSpace(*req.Net)
			}
			if req.ExpiresIn != nil {
				p.ExpiresAt = time.Time{}
				if expires > 0 {
					p.ExpiresAt = time.Now().Add(expires).UTC()
				}
			}
			if req.I1 != nil {
				is, err = s.app.PeerI1(p, settings)
				return err
			}
			return nil
		})
		return err
	})
	if err != nil {
		writeIssues(w, err, is)
		return
	}
	writeJSON(w, peerWithIssues{p, is})
}

func (s *Server) peerEgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile    string `json:"profile"`
		Killswitch bool   `json:"killswitch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	err := egress.Locked(s.app, func(e *egress.Engine) error {
		return e.Assign("", []string{name}, assignOf(req.Profile, req.Killswitch))
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
