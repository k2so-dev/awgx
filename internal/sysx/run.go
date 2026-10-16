package sysx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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
