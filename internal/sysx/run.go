package sysx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	aptLockWait  = 10 * time.Minute
	aptLockRetry = 5 * time.Second
)

type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error)
}

type Exec struct{}

func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return Exec{}.RunInput(ctx, nil, name, args...)
}

func (Exec) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	if name != "apt-get" {
		return run(ctx, input, name, args...)
	}
	args = append([]string{"-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", int(aptLockWait.Seconds()))}, args...)
	deadline := time.Now().Add(aptLockWait)
	for {
		out, err := run(ctx, input, name, args...)
		if err == nil || !strings.Contains(err.Error(), "Could not get lock") || time.Now().After(deadline) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, err
		case <-time.After(aptLockRetry):
		}
	}
}

func run(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(lastLines(stdout.String(), 5))
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func Has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

type InNS struct {
	Runner Runner
	Name   string
}

func (n InNS) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return n.RunInput(ctx, nil, name, args...)
}

func (n InNS) RunInput(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
	return n.Runner.RunInput(ctx, input, "ip", append([]string{"netns", "exec", n.Name, name}, args...)...)
}
