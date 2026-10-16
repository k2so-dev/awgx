package install

import (
	"os"
	"testing"
)

func TestCheckKey(t *testing.T) {
	key, err := os.ReadFile("testdata/ppa.asc")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKey(key, ppaFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := checkKey(key, "0000000000000000000000000000000000000000"); err == nil {
		t.Fatal("a key with another fingerprint was accepted")
	}
	if err := checkKey(append(append([]byte{}, key...), key...), ppaFingerprint); err == nil {
		t.Fatal("a keyring with several keys was accepted")
	}
	if err := checkKey([]byte("<html>"), ppaFingerprint); err == nil {
		t.Fatal("garbage was accepted")
	}
}
