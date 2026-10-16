package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
)

const jobLogLines = 300

type job struct {
	ID     int64    `json:"id"`
	Kind   string   `json:"kind"`
	Step   int      `json:"step"`
	Total  int      `json:"total"`
	Stage  string   `json:"stage"`
	Log    []string `json:"log"`
	Done   bool     `json:"done"`
	Error  string   `json:"error,omitempty"`
	Result any      `json:"result,omitempty"`
}

type jobs struct {
	mu      sync.Mutex
	cur     *job
	partial string
	next    io.Writer
}

func (j *jobs) Write(b []byte) (int, error) {
	j.mu.Lock()
	if j.cur != nil && !j.cur.Done {
		text := j.partial + string(b)
		lines := strings.Split(text, "\n")
		j.partial = lines[len(lines)-1]
		j.appendLocked(lines[:len(lines)-1]...)
	}
	j.mu.Unlock()
	if j.next != nil {
		return j.next.Write(b)
	}
	return len(b), nil
}

func (j *jobs) appendLocked(lines ...string) {
	for _, l := range lines {
		if l = strings.TrimRight(l, "\r"); l != "" {
			j.cur.Log = append(j.cur.Log, l)
		}
	}
	if n := len(j.cur.Log); n > jobLogLines {
		j.cur.Log = j.cur.Log[n-jobLogLines:]
	}
}

func (j *jobs) progress(step, total int, stage string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur != nil {
		j.cur.Step, j.cur.Total, j.cur.Stage = step, total, stage
	}
}

func (j *jobs) snapshot() *job {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur == nil {
		return nil
	}
	c := *j.cur
	c.Log = append([]string{}, j.cur.Log...)
	return &c
}

var errJobRunning = errors.New("another task is running, wait for it to finish")

func (j *jobs) start(kind string, fn func(ctx context.Context) (any, error)) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cur != nil && !j.cur.Done {
		return 0, errJobRunning
	}
	id := time.Now().UnixMilli()
	j.cur = &job{ID: id, Kind: kind, Log: []string{}}
	j.partial = ""
	go func() {
		res, err := fn(context.Background())
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.partial != "" {
			j.appendLocked(j.partial)
			j.partial = ""
		}
		j.cur.Done, j.cur.Result = true, res
		if err != nil {
			j.cur.Error = err.Error()
		}
	}()
	return id, nil
}

func (s *Server) startJob(w http.ResponseWriter, kind string, fn func(ctx context.Context, app *core.App) (any, error)) {
	id, err := s.jobs.start(kind, func(ctx context.Context) (any, error) {
		s.log.Info("web: task started", "kind", kind)
		res, err := fn(ctx, s.app.WithOut(s.jobs))
		if err != nil {
			s.log.Error("web: task failed", "kind", kind, "err", err)
		}
		return res, err
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeStatus(w, http.StatusAccepted, map[string]int64{"id": id})
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.jobs.snapshot())
}
