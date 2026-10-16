package core

import "testing"

func TestKernelOlder(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"6.8.0-142-generic", "6.8.0-146-generic", true},
		{"6.8.0-146-generic", "6.8.0-146-generic", false},
		{"6.8.0-150-generic", "6.8.0-146-generic", false},
		{"6.12.38+deb13-amd64", "6.12.48+deb13-amd64", true},
		{"6.1.0-40-amd64", "6.12.48+deb13-amd64", true},
		{"7.0.0-10-generic", "6.8.0-146-generic", false},
	} {
		if got := kernelOlder(c.a, c.b); got != c.want {
			t.Errorf("kernelOlder(%s, %s) = %v", c.a, c.b, got)
		}
	}
}
