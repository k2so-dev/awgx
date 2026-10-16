package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/setup"
	"github.com/k2so-dev/awgx/internal/state"
)

func TestJobs(t *testing.T) {
	j := &jobs{}
	release := make(chan struct{})
	if _, err := j.start("a", func(ctx context.Context) (any, error) {
		fmt.Fprint(j, "first line\nsecond ")
		fmt.Fprint(j, "line\ntail")
		j.progress(1, 2, "working")
		<-release
		return "done", errors.New("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.start("b", func(ctx context.Context) (any, error) { return nil, nil }); !errors.Is(err, errJobRunning) {
		t.Fatalf("second job started: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	snap := j.snapshot()
	if snap.Done || snap.Stage != "working" || len(snap.Log) != 2 || snap.Log[1] != "second line" {
		t.Fatalf("running job: %+v", snap)
	}
	close(release)
	for range 100 {
		if snap = j.snapshot(); snap.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !snap.Done || snap.Error != "boom" || snap.Result != "done" || snap.Log[len(snap.Log)-1] != "tail" {
		t.Fatalf("finished job: %+v", snap)
	}
	fmt.Fprint(j, "after\n")
	if len(j.snapshot().Log) != 3 {
		t.Error("output after the job ended was captured")
	}
}

func login(t *testing.T, srv *httptest.Server) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"password":"very secret password"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		CSRF string `json:"csrf"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	return c, body.CSRF
}

func call(t *testing.T, c *http.Client, method, url, csrf, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestChangePasswordNeedsCurrent(t *testing.T) {
	srv := testServer(t)
	c, csrf := login(t, srv)
	code, body := call(t, c, http.MethodPost, srv.URL+"/api/system/password", csrf, `{"current":"wrong one!!","password":"another long password"}`)
	if code != http.StatusForbidden || body["field"] != "current" {
		t.Fatalf("wrong current password: %d %v", code, body)
	}
	code, body = call(t, c, http.MethodPost, srv.URL+"/api/system/password", csrf, `{"current":"very secret password","password":"short"}`)
	if code != http.StatusBadRequest || body["field"] != "password" {
		t.Fatalf("short password: %d %v", code, body)
	}
	code, _ = call(t, c, http.MethodPost, srv.URL+"/api/system/password", csrf, `{"current":"very secret password","password":"another long password"}`)
	if code != http.StatusOK {
		t.Fatalf("change: %d", code)
	}
	if r := post(t, &http.Client{}, srv.URL+"/api/login", "", `{"password":"another long password"}`); r.StatusCode != http.StatusOK {
		t.Errorf("login with the new password: %d", r.StatusCode)
	}
}

func TestBackupPath(t *testing.T) {
	srv := testServer(t)
	c, _ := login(t, srv)
	for _, name := range []string{"..%2Fstate.db", "x.tar.zst", "%2Fetc%2Fshadow"} {
		resp, err := c.Get(srv.URL + "/api/system/backups/" + name)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
}

func TestI1Settings(t *testing.T) {
	if s, err := i1Settings("inherit", "", ""); s != nil || err != nil {
		t.Error("inherit must be nil")
	}
	if s, err := i1Settings("quic", "", ""); err != nil || s.SNI != "" {
		t.Errorf("quic must inherit the SNI: %+v %v", s, err)
	}
	if _, err := i1Settings("bogus", "", ""); err == nil {
		t.Error("unknown profile accepted")
	}
	s, err := i1Settings("quic", " example.com ", "")
	if err != nil || s.SNI != "example.com" {
		t.Errorf("quic: %+v %v", s, err)
	}
}

func TestSetupRequestPlan(t *testing.T) {
	inst := state.Instance{Name: "awg0", Endpoint: "198.51.100.1"}
	p := setup.Probe{Instance: &inst, Results: []doctor.Result{{ID: "fw", Status: doctor.Warn, Fix: &doctor.Fix{Description: "open"}}}}
	plan, err := setupRequest{Pick: []string{"clients"}, Fixes: []string{"fw"}, Clients: []string{" a ", ""}}.plan(p)
	if err != nil || len(plan.Fixes) != 0 || len(plan.Clients) != 1 || plan.Egress != setup.EgressKeep {
		t.Fatalf("clients only: %+v %v", plan, err)
	}
	plan, err = setupRequest{Pick: []string{"check", "endpoint"}, Fixes: []string{"fw", "nope"}, Endpoint: "198.51.100.1"}.plan(p)
	if err != nil || len(plan.Fixes) != 1 || plan.Endpoint != "" {
		t.Fatalf("check: %+v %v", plan, err)
	}
	on, off := true, false
	plan, err = setupRequest{Pick: []string{"ipv6"}, IPv6: &off}.plan(p)
	if err != nil || plan.IPv6 != nil {
		t.Fatalf("ipv6 unchanged: %+v %v", plan, err)
	}
	plan, err = setupRequest{Pick: []string{"ipv6"}, IPv6: &on}.plan(p)
	if err != nil || plan.IPv6 == nil || !*plan.IPv6 {
		t.Fatalf("ipv6 on: %+v %v", plan, err)
	}
	if plan, _ = (setupRequest{Pick: []string{"clients"}, IPv6: &on, Clients: []string{"a"}}).plan(p); plan.IPv6 != nil {
		t.Error("ipv6 set without the pick")
	}
	if _, err := (setupRequest{Pick: []string{"egress"}, Egress: "bogus"}).plan(p); err == nil {
		t.Error("unknown egress accepted")
	}
	if _, err := (setupRequest{Pick: []string{"egress"}, Egress: setup.EgressUplink}).plan(p); err == nil {
		t.Error("uplink without a config accepted")
	}
	if _, err := (setupRequest{Pick: []string{"clients"}, Clients: []string{"bad name"}}).plan(p); err == nil {
		t.Error("bad client name accepted")
	}
}

func TestSetIPv6Endpoint(t *testing.T) {
	srv := testServer(t)
	c, csrf := login(t, srv)
	code, _ := call(t, c, http.MethodPost, srv.URL+"/api/instances/awg0/ipv6", csrf, `{}`)
	if code != http.StatusBadRequest {
		t.Errorf("without on: %d", code)
	}
	code, body := call(t, c, http.MethodPost, srv.URL+"/api/instances/missing/ipv6", csrf, `{"on":false}`)
	if code != http.StatusBadRequest || body["error"] == nil {
		t.Errorf("unknown instance: %d %v", code, body)
	}
	if r := post(t, c, srv.URL+"/api/instances/missing/ipv6", "", `{"on":false}`); r.StatusCode != http.StatusForbidden {
		t.Errorf("without CSRF: %d", r.StatusCode)
	}
}
