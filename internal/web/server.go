package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/webconf"
)

const (
	csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
)

//go:embed assets
var assets embed.FS

type Server struct {
	app        *core.App
	log        *slog.Logger
	sessions   *sessions
	limiter    *limiter
	hashing    chan struct{}
	exposed    bool
	host       string
	started    time.Time
	docMu      sync.Mutex
	docAt      time.Time
	docRes     []doctor.Result
	docWait    chan struct{}
	jobs       *jobs
	setupProbe probeCache
}

func New(app *core.App, log *slog.Logger) *Server {
	app.LockWait = 30 * time.Second
	s := &Server{app: app, log: log, sessions: newSessions(), limiter: newLimiter(), hashing: make(chan struct{}, 2), started: time.Now(), jobs: &jobs{next: app.Out}}
	if cfg, err := webconf.LoadConfig(app); err == nil {
		s.host = cfg.Host
		if ap, err := netip.ParseAddrPort(cfg.Listen); err == nil {
			s.exposed = !ap.Addr().IsLoopback()
		}
	}
	return s
}

func (s *Server) Run(ctx context.Context, listen string) error {
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	s.log.Info("web UI listening", "addr", l.Addr().String())
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.auth(s.logout))
	mux.HandleFunc("GET /api/overview", s.auth(s.overview))
	mux.HandleFunc("POST /api/peers", s.auth(s.addPeer))
	mux.HandleFunc("DELETE /api/peers/{name}", s.auth(s.removePeer))
	mux.HandleFunc("POST /api/peers/{name}/enable", s.auth(s.enablePeer(true)))
	mux.HandleFunc("POST /api/peers/{name}/disable", s.auth(s.enablePeer(false)))
	mux.HandleFunc("GET /api/peers/{name}/export", s.auth(s.exportPeer))
	mux.HandleFunc("GET /api/stats", s.auth(s.stats))
	mux.HandleFunc("GET /api/doctor", s.auth(s.doctor(false)))
	mux.HandleFunc("POST /api/doctor", s.auth(s.doctor(true)))
	mux.HandleFunc("GET /api/egress", s.auth(s.egress))
	mux.HandleFunc("POST /api/egress/apply", s.auth(s.egressApply))
	mux.HandleFunc("POST /api/egress/reset", s.auth(s.egressReset))
	mux.HandleFunc("GET /api/journal", s.auth(s.journal))
	mux.HandleFunc("GET /api/job", s.auth(s.job))
	mux.HandleFunc("PATCH /api/peers/{name}", s.auth(s.editPeer))
	mux.HandleFunc("PUT /api/peers/{name}/egress", s.auth(s.peerEgress))
	mux.HandleFunc("POST /api/doctor/fix", s.auth(s.doctorFix))
	mux.HandleFunc("GET /api/instances", s.auth(s.instances))
	mux.HandleFunc("GET /api/presets", s.auth(s.presets))
	mux.HandleFunc("POST /api/instances", s.auth(s.addInstance))
	mux.HandleFunc("DELETE /api/instances/{name}", s.auth(s.removeInstance))
	mux.HandleFunc("PUT /api/instances/{name}/endpoint", s.auth(s.setEndpoint))
	mux.HandleFunc("PUT /api/instances/{name}/mtu", s.auth(s.setMTU))
	mux.HandleFunc("PUT /api/instances/{name}/rate", s.auth(s.setRate))
	mux.HandleFunc("POST /api/instances/{name}/ipv6", s.auth(s.setIPv6))
	mux.HandleFunc("PUT /api/instances/{name}/i1", s.auth(s.setInstanceI1))
	mux.HandleFunc("POST /api/instances/{name}/i1/rotate", s.auth(s.rotateI1))
	mux.HandleFunc("PUT /api/instances/{name}/obfuscation", s.auth(s.setObfuscation))
	mux.HandleFunc("POST /api/egress/profiles", s.auth(s.saveProfile(false)))
	mux.HandleFunc("PUT /api/egress/profiles/{name}", s.auth(s.saveProfile(true)))
	mux.HandleFunc("DELETE /api/egress/profiles/{name}", s.auth(s.removeProfile))
	mux.HandleFunc("PUT /api/egress/assign", s.auth(s.assign))
	mux.HandleFunc("POST /api/egress/history/{id}/rollback", s.auth(s.rollbackTo))
	mux.HandleFunc("POST /api/uplinks", s.auth(s.addUplink))
	mux.HandleFunc("GET /api/uplinks/{name}", s.auth(s.uplinkConf))
	mux.HandleFunc("PUT /api/uplinks/{name}", s.auth(s.updateUplink))
	mux.HandleFunc("DELETE /api/uplinks/{name}", s.auth(s.removeUplink))
	mux.HandleFunc("POST /api/uplinks/{name}/test", s.auth(s.testUplink))
	mux.HandleFunc("POST /api/uplinks/{name}/speed", s.auth(s.speedUplink))
	mux.HandleFunc("GET /api/xui", s.auth(s.xuiStatus))
	mux.HandleFunc("POST /api/xui/connect", s.auth(s.xuiConnect))
	mux.HandleFunc("POST /api/xui/disconnect", s.auth(s.xuiDisconnect))
	mux.HandleFunc("GET /api/system", s.auth(s.system))
	mux.HandleFunc("PUT /api/system/settings", s.auth(s.systemSettings))
	mux.HandleFunc("POST /api/system/totp", s.auth(s.newTOTP))
	mux.HandleFunc("POST /api/system/password", s.auth(s.changePassword))
	mux.HandleFunc("POST /api/system/update/check", s.auth(s.checkUpdate))
	mux.HandleFunc("POST /api/system/update/self", s.auth(s.updateSelf))
	mux.HandleFunc("POST /api/system/update/modules", s.auth(s.updateModules))
	mux.HandleFunc("POST /api/system/backup", s.auth(s.createBackup))
	mux.HandleFunc("GET /api/system/backups/{file}", s.auth(s.downloadBackup))
	mux.HandleFunc("POST /api/system/restore", s.auth(s.restore))
	mux.HandleFunc("GET /api/setup", s.auth(s.setupInfo))
	mux.HandleFunc("POST /api/setup/summary", s.auth(s.setupSummary))
	mux.HandleFunc("POST /api/setup", s.auth(s.setupRun))
	return s.headers(mux)
}

func (s *Server) proxied(r *http.Request) bool {
	return s.host != "" && remoteLoopback(r)
}

func (s *Server) requestHost(r *http.Request) string {
	if s.proxied(r) {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			return h
		}
	}
	return r.Host
}

func (s *Server) allowedHost(r *http.Request) bool {
	host := s.requestHost(r)
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.Trim(name, "[]")
	if name == "localhost" || (s.host != "" && strings.EqualFold(name, s.host)) {
		return true
	}
	a, err := netip.ParseAddr(name)
	return err == nil && (a.IsLoopback() || (s.exposed && s.host == ""))
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedHost(r) {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	return strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://") == s.requestHost(r)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessions.get(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("login required"))
			return
		}
		if r.Method != http.MethodGet {
			if !s.sameOrigin(r) || r.Header.Get("X-CSRF-Token") != sess.csrf {
				writeError(w, http.StatusForbidden, errors.New("CSRF check failed"))
				return
			}
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeStatus(w, code, map[string]string{"error": err.Error()})
}

func writeStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	return json.NewDecoder(r.Body).Decode(v)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	cfg, _ := webconf.LoadConfig(s.app)
	sess, ok := s.sessions.get(r)
	writeJSON(w, map[string]any{"authenticated": ok, "csrf": sess.csrf, "totp": cfg.TOTP, "started_at": s.started.UTC()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if d := s.limiter.blocked(ip); d > 0 {
		w.Header().Set("Retry-After", retryAfter(d))
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts, try again later"))
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, errors.New("cross-origin login"))
		return
	}
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		writeError(w, http.StatusTooManyRequests, errors.New("busy, try again"))
		return
	}
	s.limiter.fail(ip)
	ok, err := s.checkCredentials(req.Password, strings.TrimSpace(req.Code))
	if err != nil {
		s.log.Error("web login check failed", "err", err)
	}
	if !ok {
		s.log.Warn("web login failed", "ip", ip)
		time.Sleep(300 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, errors.New("wrong password or code"))
		return
	}
	s.limiter.ok(ip)
	id, sess := s.sessions.create()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: s.secure(r),
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	s.log.Info("web login", "ip", ip)
	writeJSON(w, map[string]any{"authenticated": true, "csrf": sess.csrf})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sessions.drop(r)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) checkCredentials(password, code string) (bool, error) {
	store, err := s.app.Store()
	if err != nil {
		return false, err
	}
	hash, err := store.Setting(webconf.KeyPassword)
	if err != nil || hash == "" || !webconf.VerifyPassword(hash, password) {
		return false, err
	}
	var secret string
	ok, err := store.SecretSetting(webconf.SecretTOTP, &secret)
	if err != nil || !ok {
		return err == nil, err
	}
	step, ok := totpStep(secret, code, time.Now())
	return ok && s.limiter.useStep(step), nil
}
