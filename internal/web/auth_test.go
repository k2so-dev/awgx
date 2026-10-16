package web

import (
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/webconf"
)

func TestPassword(t *testing.T) {
	h, err := webconf.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !webconf.VerifyPassword(h, "correct horse battery") {
		t.Error("valid password rejected")
	}
	if webconf.VerifyPassword(h, "wrong password!") {
		t.Error("wrong password accepted")
	}
	if _, err := webconf.HashPassword("short"); err == nil {
		t.Error("short password accepted")
	}
}

func TestTOTP(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if got := TOTPCode(secret, time.Unix(59, 0)); got != "287082" {
		t.Errorf("RFC 6238 vector: got %s", got)
	}
	now := time.Unix(1_700_000_000, 0)
	if !VerifyTOTP(secret, TOTPCode(secret, now.Add(-30*time.Second)), now) {
		t.Error("previous step rejected")
	}
	if VerifyTOTP(secret, TOTPCode(secret, now.Add(-90*time.Second)), now) {
		t.Error("old code accepted")
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	for range maxFailures {
		l.fail("1.2.3.4")
	}
	if l.blocked("1.2.3.4") == 0 {
		t.Error("not blocked after failures")
	}
	if l.blocked("5.6.7.8") != 0 {
		t.Error("other ip blocked")
	}
}
