package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionTTL  = 12 * time.Hour
	cookieName  = "awgx_session"
	maxFailures = 5
)

func NewTOTPSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func TOTPCode(secret string, t time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(t.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

func VerifyTOTP(secret, code string, now time.Time) bool {
	_, ok := totpStep(secret, code, now)
	return ok
}

func totpStep(secret, code string, now time.Time) (int64, bool) {
	if secret == "" || len(code) != 6 {
		return 0, false
	}
	for _, d := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		t := now.Add(d)
		want := TOTPCode(secret, t)
		if want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return t.Unix() / 30, true
		}
	}
	return 0, false
}

func TOTPURL(secret, host string) string {
	return "otpauth://totp/awgx:" + host + "?secret=" + secret + "&issuer=awgx"
}

type session struct {
	csrf    string
	expires time.Time
}

type sessions struct {
	mu   sync.Mutex
	byID map[string]session
}

func newSessions() *sessions {
	return &sessions{byID: map[string]session{}}
}

func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessions) create() (string, session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, v := range s.byID {
		if now.After(v.expires) {
			delete(s.byID, id)
		}
	}
	id := token()
	v := session{csrf: token(), expires: now.Add(sessionTTL)}
	s.byID[id] = v
	return id, v
}

func (s *sessions) get(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byID[c.Value]
	if !ok || time.Now().After(v.expires) {
		delete(s.byID, c.Value)
		return session{}, false
	}
	return v, true
}

func (s *sessions) drop(r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.byID, c.Value)
		s.mu.Unlock()
	}
}

func (s *sessions) keepOnly(r *http.Request) {
	c, _ := r.Cookie(cookieName)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.byID {
		if c == nil || id != c.Value {
			delete(s.byID, id)
		}
	}
}

type limiter struct {
	mu       sync.Mutex
	fails    map[string]int
	until    map[string]time.Time
	lastStep int64
}

func newLimiter() *limiter {
	return &limiter{fails: map[string]int{}, until: map[string]time.Time{}}
}

func remoteLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.proxied(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			host = strings.TrimSpace(parts[len(parts)-1])
		} else if real := r.Header.Get("X-Real-IP"); real != "" {
			host = strings.TrimSpace(real)
		}
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	a = a.Unmap()
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

func (l *limiter) useStep(step int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if step <= l.lastStep {
		return false
	}
	l.lastStep = step
	return true
}

func (l *limiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := time.Until(l.until[ip]); d > 0 {
		return d
	}
	return 0
}

func (l *limiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) > 10000 {
		l.prune()
	}
	l.fails[ip]++
	if n := l.fails[ip]; n >= maxFailures {
		lock := time.Minute << min(n-maxFailures, 6)
		l.until[ip] = time.Now().Add(lock)
	}
}

func (l *limiter) prune() {
	now := time.Now()
	for ip := range l.fails {
		if until, ok := l.until[ip]; !ok || until.Before(now) {
			delete(l.fails, ip)
			delete(l.until, ip)
		}
	}
}

func (l *limiter) ok(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
	delete(l.until, ip)
}

func retryAfter(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds()) + 1)
}
