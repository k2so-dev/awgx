package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/netx"
	"github.com/k2so-dev/awgx/internal/render"
	"github.com/k2so-dev/awgx/internal/secrets"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	DefaultInstance = "awg0"
)

var ErrNotInstalled = errors.New("awgx is not installed, run: awgx install")

var ErrNoInstances = errors.New("no instances, add one: awgx instance add NAME")

var ErrBusy = errors.New("another awgx task is changing the state, try again when it finishes")

type Paths struct {
	StateDir  string
	ConfDir   string
	SysctlDir string
	BackupDir string
	DataDir   string
	Binary    string
}

var DefaultPaths = Paths{
	StateDir:  "/etc/awgx",
	ConfDir:   awg.ConfDir,
	SysctlDir: "/etc/sysctl.d",
	BackupDir: "/var/lib/awgx/backups",
	DataDir:   "/var/lib/awgx",
	Binary:    render.Binary,
}

func (p Paths) StateDB() string {
	return filepath.Join(p.StateDir, "state.db")
}

func (p Paths) KeyFile() string {
	return filepath.Join(p.StateDir, "key")
}

func (p Paths) Conf(name string) string {
	return filepath.Join(p.ConfDir, name+".conf")
}

type App struct {
	Paths    Paths
	Run      sysx.Runner
	Out      io.Writer
	Selected string
	Version  string
	Source   string
	LockWait time.Duration
	db       *storeRef
}

type storeRef struct {
	mu    sync.Mutex
	store *state.Store
}

func New() *App {
	return &App{Paths: DefaultPaths, Run: sysx.Exec{}, Out: os.Stderr, db: &storeRef{}}
}

func (a *App) Logf(format string, args ...any) {
	fmt.Fprintf(a.Out, format+"\n", args...)
}

func (a *App) Tool() awg.Tool {
	return awg.Tool{Run: a.NS()}
}

func (a *App) Installed() bool {
	return sysx.Exists(a.Paths.StateDB())
}

func (a *App) Store() (*state.Store, error) {
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	if a.db.store != nil {
		return a.db.store, nil
	}
	if !a.Installed() {
		return nil, ErrNotInstalled
	}
	return a.open()
}

func (a *App) Init() (*state.Store, error) {
	if err := os.MkdirAll(a.Paths.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(a.Paths.StateDir, 0o700); err != nil {
		return nil, err
	}
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	return a.open()
}

func (a *App) open() (*state.Store, error) {
	box, err := secrets.LoadOrCreate(a.Paths.KeyFile())
	if err != nil {
		return nil, err
	}
	s, err := state.Open(a.Paths.StateDB(), box)
	if err != nil {
		return nil, err
	}
	s.Source = a.Source
	a.db.store = s
	return s, nil
}

func (a *App) Fork(source string) *App {
	n := *a
	n.Source, n.db = source, &storeRef{}
	return &n
}

func (a *App) WithOut(w io.Writer) *App {
	n := *a
	n.Out = w
	return &n
}

func (a *App) Close() {
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	if a.db.store != nil {
		a.db.store.Close()
		a.db.store = nil
	}
}

func (a *App) Lock() (func(), error) {
	if a.LockWait == 0 {
		return a.LockContext(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.LockWait)
	defer cancel()
	unlock, err := a.LockContext(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrBusy
	}
	return unlock, err
}

func (a *App) Locked(fn func() error) error {
	unlock, err := a.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (a *App) LockContext(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(a.Paths.StateDir, 0o700); err != nil {
		return nil, err
	}
	return state.LockContext(ctx, a.Paths.StateDir)
}

func (a *App) Instance(name string) (state.Instance, error) {
	s, err := a.Store()
	if err != nil {
		return state.Instance{}, err
	}
	return s.Instance(name)
}

func (a *App) InterfaceUp(name string) bool {
	return netx.LinkExists(name)
}

func (a *App) Journal(action, format string, args ...any) {
	a.db.mu.Lock()
	s := a.db.store
	a.db.mu.Unlock()
	if s != nil {
		s.Log(action, fmt.Sprintf(format, args...))
	}
}

const (
	ServeUnit          = "awgx-serve.service"
	SettingUpdateCheck = "update_check"
)

func (a *App) ServeEnabled(ctx context.Context) bool {
	out, _ := a.Run.Run(ctx, "systemctl", "is-enabled", ServeUnit)
	return strings.TrimSpace(string(out)) == "enabled"
}

func (a *App) SetServe(ctx context.Context, on bool) error {
	action := "disable"
	if on {
		action = "enable"
	}
	if _, err := a.Run.Run(ctx, "systemctl", action, "--now", ServeUnit); err != nil {
		return fmt.Errorf("%s %s: %w", action, ServeUnit, err)
	}
	return nil
}

func (a *App) StartServe(ctx context.Context) error {
	if _, err := a.Run.Run(ctx, "systemctl", "is-active", "--quiet", ServeUnit); err != nil {
		return a.SetServe(ctx, true)
	}
	if _, err := a.Run.Run(ctx, "systemctl", "enable", ServeUnit); err != nil {
		return fmt.Errorf("enable %s: %w", ServeUnit, err)
	}
	return a.RestartServe(ctx)
}

func (a *App) RestartServe(ctx context.Context) error {
	action := "restart"
	if !a.ServeEnabled(ctx) {
		action = "try-restart"
	}
	if _, err := a.Run.Run(ctx, "systemctl", action, ServeUnit); err != nil {
		return fmt.Errorf("restart %s: %w", ServeUnit, err)
	}
	return nil
}
