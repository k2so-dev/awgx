package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/webconf"
)

func label(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func forwarded(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded", "X-Forwarded-Host"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	cfg, err := webconf.LoadConfig(s.app)
	if err != nil || !cfg.Metrics || !remoteLoopback(r) || forwarded(r) {
		http.NotFound(w, r)
		return
	}
	rows, err := s.app.PeerRows(r.Context(), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	b.WriteString("# TYPE awgx_peer_receive_bytes_total counter\n")
	for _, p := range rows {
		fmt.Fprintf(&b, "awgx_peer_receive_bytes_total{iface=\"%s\",peer=\"%s\"} %d\n", label(p.Instance), label(p.Name), p.RxBytes)
	}
	b.WriteString("# TYPE awgx_peer_transmit_bytes_total counter\n")
	for _, p := range rows {
		fmt.Fprintf(&b, "awgx_peer_transmit_bytes_total{iface=\"%s\",peer=\"%s\"} %d\n", label(p.Instance), label(p.Name), p.TxBytes)
	}
	b.WriteString("# TYPE awgx_peer_last_handshake_seconds gauge\n")
	for _, p := range rows {
		var ts int64
		if !p.LastHandshake.IsZero() {
			ts = p.LastHandshake.Unix()
		}
		fmt.Fprintf(&b, "awgx_peer_last_handshake_seconds{iface=\"%s\",peer=\"%s\"} %d\n", label(p.Instance), label(p.Name), ts)
	}
	var online int
	for _, p := range rows {
		if p.Online {
			online++
		}
	}
	fmt.Fprintf(&b, "# TYPE awgx_peers gauge\nawgx_peers %d\n# TYPE awgx_peers_online gauge\nawgx_peers_online %d\n", len(rows), online)
	if e, err := egress.New(s.app); err == nil {
		applied, _ := e.Store.Applied()
		health := e.Health()
		var up, degraded strings.Builder
		for _, p := range applied.Config.Profiles {
			h, _ := egress.ProfileHealth(applied.Config, health, p.Name)
			u, d := 1, 0
			if h == egress.HealthDown {
				u = 0
			}
			if h == egress.HealthDegraded {
				d = 1
			}
			fmt.Fprintf(&up, "awgx_egress_up{profile=\"%s\"} %d\n", label(p.Name), u)
			fmt.Fprintf(&degraded, "awgx_egress_degraded{profile=\"%s\"} %d\n", label(p.Name), d)
		}
		b.WriteString("# TYPE awgx_egress_up gauge\n" + up.String() + "# TYPE awgx_egress_degraded gauge\n" + degraded.String())
	}
	w.Write([]byte(b.String()))
}
