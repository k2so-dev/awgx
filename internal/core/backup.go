package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k2so-dev/awgx/internal/backup"
)

const KeepBackups = 20

func (a *App) Backup(dir string) (string, error) {
	name, err := a.backup(dir)
	if err == nil && dir == "" {
		backup.Prune(a.Paths.BackupDir, KeepBackups)
	}
	return name, err
}

func (a *App) backup(dir string) (string, error) {
	if !a.Installed() {
		return "", ErrNotInstalled
	}
	s, err := a.Store()
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = a.Paths.BackupDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(dir, ".snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "state.db")
	if _, err := s.Exec("VACUUM INTO ?", snap); err != nil {
		return "", err
	}
	return backup.Create(dir, a.BackupPaths(), map[string]string{a.Paths.StateDB(): snap})
}

func (a *App) BackupPaths() []string {
	return []string{a.Paths.StateDir, a.Paths.ConfDir}
}

func (a *App) Restore(ctx context.Context, archive string) error {
	if err := backup.Verify(archive, a.BackupPaths()); err != nil {
		return err
	}
	unlock, err := a.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := a.backup("")
	if err != nil {
		return fmt.Errorf("backup of the current state: %w", err)
	}
	before := a.InstanceNames()
	if _, err := a.Run.Run(ctx, "systemctl", "is-active", "--quiet", ServeUnit); err == nil {
		a.Run.Run(ctx, "systemctl", "stop", ServeUnit)
		defer a.Run.Run(ctx, "systemctl", "start", ServeUnit)
	}
	a.Close()
	os.Remove(a.Paths.StateDB() + "-wal")
	os.Remove(a.Paths.StateDB() + "-shm")
	if _, err := backup.Restore(archive, "/", a.BackupPaths()); err != nil {
		if _, rerr := backup.Restore(current, "/", a.BackupPaths()); rerr != nil {
			return fmt.Errorf("%w; returning to %s also failed: %v", err, current, rerr)
		}
		os.Remove(current)
		return fmt.Errorf("%w; the previous state is back", err)
	}
	if err := a.RestartInstances(ctx, before); err != nil {
		return fmt.Errorf("%w (the previous state is in %s)", err, current)
	}
	os.Remove(current)
	if _, err := a.Store(); err == nil {
		a.Journal("restore", "%s", archive)
	}
	return nil
}
