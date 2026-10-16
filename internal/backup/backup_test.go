package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "etc"), 0o700)
	os.WriteFile(filepath.Join(src, "etc", "state.db"), []byte("state"), 0o600)
	os.WriteFile(filepath.Join(src, "etc", "key"), []byte("key"), 0o600)
	os.MkdirAll(filepath.Join(src, "other"), 0o700)
	os.WriteFile(filepath.Join(src, "other", "x"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(src, "etc", "state.db-wal"), []byte("wal"), 0o600)
	snap := filepath.Join(t.TempDir(), "snap.db")
	os.WriteFile(snap, []byte("snapshot"), 0o644)
	archive, err := Create(t.TempDir(), []string{filepath.Join(src, "etc"), filepath.Join(src, "missing"), filepath.Join(src, "other")},
		map[string]string{filepath.Join(src, "etc", "state.db"): snap})
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(archive); fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode %v", fi.Mode())
	}
	dst := t.TempDir()
	bad := t.TempDir()
	if _, err := Restore(archive, bad, []string{filepath.Join(src, "etc")}); err == nil {
		t.Fatal("entries outside the allowed directories were restored")
	}
	if entries, _ := os.ReadDir(bad); len(entries) != 0 {
		t.Fatalf("a rejected archive was partly restored: %v", entries)
	}
	files, err := Restore(archive, dst, []string{filepath.Join(src, "etc"), filepath.Join(src, "other")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("restored %v", files)
	}
	b, _ := os.ReadFile(filepath.Join(dst, src, "etc", "state.db"))
	if string(b) != "snapshot" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(dst, src, "etc", "state.db")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("state.db mode %v", fi.Mode())
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		name := filepath.Join(dir, fmt.Sprintf("awgx-backup-%d.tar.zst", i))
		os.WriteFile(name, nil, 0o600)
		at := time.Now().Add(time.Duration(i) * time.Minute)
		os.Chtimes(name, at, at)
	}
	if err := Prune(dir, 2); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || filepath.Base(left[0]) != "awgx-backup-3.tar.zst" {
		t.Fatalf("left %v", left)
	}
}

func TestCreateRemovesPartial(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	src, dir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "secret"), []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, []string{src}, nil); err == nil {
		t.Fatal("unreadable file accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("partial archive left: %v", left)
	}
}
