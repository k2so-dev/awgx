package xui

import (
	"errors"
	"testing"
)

func TestParseSettings(t *testing.T) {
	out := "current panel settings as follows:\nPanel is secure with SSL\nhasDefaultCredential: false\nport: 2053\nwebBasePath: /xpanel/\n"
	port, base, secure := ParseSettings(out)
	if port != 2053 || base != "/xpanel/" || !secure {
		t.Fatalf("got %d %q %v", port, base, secure)
	}
	_, base, secure = ParseSettings("Warning: Panel is not secure with SSL\nport: 1\nwebBasePath: x\n")
	if base != "/x/" || secure {
		t.Fatalf("got %q %v", base, secure)
	}
}

func TestCheckVersion(t *testing.T) {
	if err := CheckVersion("3.8.5"); err != nil {
		t.Error(err)
	}
	if err := CheckVersion("1.9.0"); err == nil {
		t.Error("old version accepted")
	}
	if err := CheckVersion("4.0.0"); !errors.Is(err, ErrUntested) {
		t.Errorf("4.x: %v", err)
	}
}

func TestCheckPanelURL(t *testing.T) {
	for url, ok := range map[string]bool{
		"http://127.0.0.1:2053/x/":   true,
		"https://panel.example.com/": true,
		"http://panel.example.com/":  false,
		"ftp://127.0.0.1/":           false,
	} {
		if err := CheckPanelURL(url); (err == nil) != ok {
			t.Errorf("%s: %v", url, err)
		}
	}
}
