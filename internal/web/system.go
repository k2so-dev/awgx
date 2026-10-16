package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/backup"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/qr"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
	"github.com/k2so-dev/awgx/internal/upgrade"
	"github.com/k2so-dev/awgx/internal/webconf"
)

type backupFile struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

func (s *Server) backups() []backupFile {
	out := []backupFile{}
	entries, _ := os.ReadDir(s.app.Paths.BackupDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.zst") {
			continue
		}
		if info, err := e.Info(); err == nil {
			out = append(out, backupFile{e.Name(), info.Size(), info.ModTime().UTC()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (s *Server) backupPath(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || !strings.HasSuffix(name, ".tar.zst") {
		return "", errors.New("unknown backup")
	}
	p := filepath.Join(s.app.Paths.BackupDir, name)
	if !sysx.Exists(p) {
		return "", errors.New("unknown backup")
	}
	return p, nil
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cfg, _ := webconf.LoadConfig(s.app)
	check, _ := store.Setting(core.SettingUpdateCheck)
	update, _ := store.Setting(state.SettingUpdate)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	modules, _ := upgrade.Versions(ctx, s.app.Run)
	if modules == nil {
		modules = map[string]string{}
	}
	var endpoint string
	if inst, err := s.defaultInstance(); err == nil {
		endpoint = inst.Endpoint
	}
	mode := "kernel"
	if !sysx.Exists("/sys/module/amneziawg") {
		mode = "module not loaded"
	}
	writeJSON(w, map[string]any{
		"version": s.app.Version, "kernel": sysx.KernelRelease(), "mode": mode, "modules": modules,
		"update_check": check != "off", "update": update,
		"web": cfg, "tunnel": webconf.Tunnel(cfg.Listen, endpoint),
		"backups": s.backups(), "backup_dir": s.app.Paths.BackupDir, "keep_backups": core.KeepBackups,
	})
}

func (s *Server) systemSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UpdateCheck *bool `json:"update_check"`
		Metrics     *bool `json:"metrics"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var err error
	if req.UpdateCheck != nil {
		err = upgrade.SetAutoCheck(s.app, *req.UpdateCheck)
	}
	if err == nil && req.Metrics != nil {
		err = webconf.SetMetrics(s.app, *req.Metrics)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) newTOTP(w http.ResponseWriter, r *http.Request) {
	secret := NewTOTPSecret()
	host, _ := os.Hostname()
	url := TOTPURL(secret, host)
	png, err := qr.PNG(url, 256)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"secret": secret, "url": url, "qr": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current  string `json:"current"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
		Secret   string `json:"secret"`
		Code     string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	store, err := s.app.Store()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	select {
	case s.hashing <- struct{}{}:
		defer func() { <-s.hashing }()
	default:
		writeError(w, http.StatusTooManyRequests, errors.New("busy, try again"))
		return
	}
	hash, _ := store.Setting(webconf.KeyPassword)
	if hash == "" || !webconf.VerifyPassword(hash, req.Current) {
		time.Sleep(300 * time.Millisecond)
		writeStatus(w, http.StatusForbidden, map[string]string{"error": "the current password is wrong", "field": "current"})
		return
	}
	if req.TOTP == "new" && !VerifyTOTP(req.Secret, strings.TrimSpace(req.Code), time.Now()) {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": "wrong code from the authenticator", "field": "code"})
		return
	}
	if req.Password != "" {
		if err := webconf.SetPassword(s.app, req.Password); err != nil {
			writeStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "field": "password"})
			return
		}
	}
	switch req.TOTP {
	case "off":
		err = store.DeleteSecret(webconf.SecretTOTP)
	case "new":
		err = store.SetSecretSetting(webconf.SecretTOTP, req.Secret)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.TOTP == "off" || req.TOTP == "new" {
		store.Log("web", "TOTP "+map[string]string{"off": "removed", "new": "enabled"}[req.TOTP])
	}
	s.sessions.keepOnly(r)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tag, err := upgrade.CheckUpdate(ctx, s.app)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]string{"latest": tag, "current": s.app.Version})
}

func (s *Server) updateSelf(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "update.self", func(ctx context.Context, app *core.App) (any, error) {
		s.jobs.progress(1, 2, "downloading and verifying")
		tag, err := upgrade.SelfUpdate(ctx, app, "")
		if err != nil {
			return nil, err
		}
		if tag == "" {
			app.Logf("awgx %s is up to date", app.Version)
			return nil, nil
		}
		s.jobs.progress(2, 2, "restarting the data plane and awgx-serve")
		app.Logf("awgx upgraded to %s, restarting", tag)
		go func() {
			time.Sleep(time.Second)
			app.Run.Run(context.Background(), "systemctl", "--no-block", "try-restart", core.DataplaneUnit, core.ServeUnit)
		}()
		return map[string]bool{"restart": true}, nil
	})
}

func (s *Server) updateModules(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "update.modules", func(ctx context.Context, app *core.App) (any, error) {
		unlock, err := app.Lock()
		if err != nil {
			return nil, err
		}
		defer unlock()
		res, err := upgrade.Modules(ctx, app)
		var changed []string
		for pkg, v := range res.After {
			if res.Before[pkg] != v {
				changed = append(changed, fmt.Sprintf("%s %s -> %s", pkg, res.Before[pkg], v))
			}
		}
		for _, c := range changed {
			app.Logf("%s", c)
		}
		if err == nil && len(changed) == 0 {
			app.Logf("module and tools are up to date")
		}
		return nil, err
	})
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	name, err := s.app.Backup("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]string{"name": filepath.Base(name)})
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	p, err := s.backupPath(r.PathValue("file"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	s.log.Info("web: backup downloaded", "file", filepath.Base(p))
	http.ServeFile(w, r, p)
}

func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File    string `json:"file"`
		Confirm string `json:"confirm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Confirm != "restore" {
		writeStatus(w, http.StatusBadRequest, map[string]string{"error": "type restore to confirm", "field": "confirm"})
		return
	}
	p, err := s.backupPath(req.File)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if snap := s.jobs.snapshot(); snap != nil && !snap.Done {
		writeError(w, http.StatusConflict, errJobRunning)
		return
	}
	if err := backup.Verify(p, s.app.BackupPaths()); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("web: restore started", "file", req.File)
	out, err := s.app.Run.Run(context.WithoutCancel(r.Context()), "systemd-run", "--unit=awgx-restore", "--collect", "--quiet", "--setenv=AWGX_SOURCE=web",
		s.app.Paths.Binary, "restore", p, "--yes")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out))))
		return
	}
	writeStatus(w, http.StatusAccepted, map[string]bool{"ok": true})
}
