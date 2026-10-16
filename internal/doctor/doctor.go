package doctor

import (
	"context"
	"os"
	"path/filepath"

	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

type Fix struct {
	Description string                          `json:"description"`
	Apply       func(ctx context.Context) error `json:"-"`
}

type Result struct {
	ID      string `json:"id"`
	Group   string `json:"group"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Fix     *Fix   `json:"fix,omitempty"`
}

type Env struct {
	Run        sysx.Runner
	OS         sysx.OS
	Kernel     string
	SysctlDir  string
	Store      *state.Store
	Instance   *state.Instance
	Instances  []state.Instance
	Failed     []string
	Reconcile  func(ctx context.Context, name string) error
	ApplyRate  func(ctx context.Context, name string) error
	StartServe func(ctx context.Context) error
}

type check struct {
	id    string
	group string
	run   func(ctx context.Context, e *Env) Result
	each  bool
}

func Run(ctx context.Context, e *Env) []Result {
	var out []Result
	for _, c := range checks {
		if c.each && len(e.Instances) > 1 {
			for _, inst := range e.Instances {
				one := *e
				one.Instance = &inst
				r := c.run(ctx, &one)
				r.ID = c.id + ":" + inst.Name
				r.Group = c.group
				out = append(out, r)
			}
			continue
		}
		r := c.run(ctx, e)
		r.ID = c.id
		r.Group = c.group
		out = append(out, r)
	}
	return out
}

func ExitCode(results []Result) int {
	code := 0
	for _, r := range results {
		switch r.Status {
		case Fail:
			return 2
		case Warn:
			code = 1
		}
	}
	return code
}

func Failed(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Status == Fail {
			out = append(out, r)
		}
	}
	return out
}

func ok(format string) Result {
	return Result{Status: OK, Message: format}
}

func warn(msg string, fix *Fix) Result {
	return Result{Status: Warn, Message: msg, Fix: fix}
}

func fail(msg string, fix *Fix) Result {
	return Result{Status: Fail, Message: msg, Fix: fix}
}

func (e *Env) dropIn(name string, lines ...string) *Fix {
	path := filepath.Join(e.SysctlDir, "90-awgx-"+name+".conf")
	return &Fix{
		Description: "write " + path + " and apply it",
		Apply: func(ctx context.Context) error {
			content := ""
			for _, l := range lines {
				content += l + "\n"
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
			_, err := e.Run.Run(ctx, "sysctl", "-p", path)
			return err
		},
	}
}
