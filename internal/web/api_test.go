package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestParseChecks(t *testing.T) {
	got := parseChecks([]string{
		"ok   via-b: via awg1 TCP and DNS work",
		"FAIL post-check: timeout",
		"ok   all peers use direct egress",
	})
	want := []check{
		{"ok", "via-b", "via awg1 TCP and DNS work"},
		{"fail", "post-check", "timeout"},
		{"ok", "", "all peers use direct egress"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAddPeerFieldError(t *testing.T) {
	srv := testServer(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"very secret password"}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/peers", strings.NewReader(`{"name":"bad name"}`))
	req.Header.Set("X-CSRF-Token", login.CSRF)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusBadRequest || body["field"] != "name" {
		t.Errorf("got %d %v", resp.StatusCode, body)
	}
}

func TestInputIssues(t *testing.T) {
	is := inputIssues(errors.New("S1: not a number"))
	if len(is) != 1 || is[0].Field != "S1" || is[0].Message != "not a number" {
		t.Fatalf("got %+v", is)
	}
	if is := inputIssues(errors.New("bad")); is[0].Field != "input" {
		t.Fatalf("got %+v", is)
	}
}
