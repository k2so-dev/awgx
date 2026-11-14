package sysx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAptGetWaitsForLock(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
echo "$@" >> "` + dir + `/calls"
[ "$(wc -l < "` + dir + `/calls")" -ge 3 ] && exit 0
echo "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1 (unattended-upgr)" >&2
exit 100
`
	if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	aptLockRetry = time.Millisecond
	if _, err := (Exec{}).Run(context.Background(), "apt-get", "update"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 3 || lines[0] != "-o DPkg::Lock::Timeout=600 update" {
		t.Fatalf("calls: %q", lines)
	}
}

func TestAptGetOtherErrorsFailAtOnce(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho x >> " + dir + "/calls\necho 'E: Unable to locate package nope' >&2\nexit 100\n"
	if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	aptLockRetry = time.Millisecond
	if _, err := (Exec{}).Run(context.Background(), "apt-get", "install", "-y", "nope"); err == nil {
		t.Fatal("want error")
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if n := strings.Count(string(calls), "x"); n != 1 {
		t.Fatalf("calls: %d", n)
	}
}
