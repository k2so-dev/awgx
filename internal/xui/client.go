package xui

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	Binary     = "/usr/local/x-ui/x-ui"
	DB         = "/etc/x-ui/x-ui.db"
	Unit       = "x-ui"
	Tag        = "awgx-egress"
	MinMajor   = 3
	MaxMajor   = 3
	TokenName  = "cli-fallback"
	apiPrefix  = "panel/api/"
	maxRespLen = 4 << 20
)

type Panel struct {
	URL     string `json:"url"`
	Version string `json:"version"`
}

type Auth struct {
	Token    string `json:"token,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

type Client struct {
	Panel Panel
	Auth  Auth
	http  *http.Client
	csrf  string
	host  string
}

func Installed() bool {
	return sysx.Exists(Binary)
}

func Version(ctx context.Context, run sysx.Runner) (string, error) {
	out, err := run.Run(ctx, Binary, "-v")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), nil
}

func Major(v string) int {
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

var ErrUntested = errors.New("untested 3x-ui API")

func CheckVersion(v string) error {
	m := Major(v)
	if m < MinMajor {
		return fmt.Errorf("3x-ui %s is too old, need %d.x or newer", v, MinMajor)
	}
	if m > MaxMajor {
		return fmt.Errorf("3x-ui %s: %w (awgx supports up to %d.x); egress is left as is", v, ErrUntested, MaxMajor)
	}
	return nil
}

func CheckPanelURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid panel URL %q", raw)
	}
	if u.Scheme != "https" && !loopback(u.Hostname()) {
		return fmt.Errorf("panel %s is not on this host: use https so the token is not sent in clear", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("invalid panel URL scheme %q", u.Scheme)
	}
	return nil
}

func Discover(ctx context.Context, run sysx.Runner) (Panel, error) {
	var p Panel
	if !Installed() {
		return p, errors.New("3x-ui is not installed on this host (no " + Binary + ")")
	}
	var err error
	if p.Version, err = Version(ctx, run); err != nil {
		return p, err
	}
	if err := CheckVersion(p.Version); err != nil {
		return p, err
	}
	out, err := run.Run(ctx, Binary, "setting", "-show")
	if err != nil {
		return p, err
	}
	port, base, secure := ParseSettings(string(out))
	if port == 0 {
		return p, errors.New("cannot read the 3x-ui panel port, pass --panel")
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	p.URL = fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, port, base)
	return p, nil
}

func ParseSettings(out string) (port int, base string, secure bool) {
	base = "/"
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "port:"):
			port, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "port:")))
		case strings.HasPrefix(l, "webBasePath:"):
			base = strings.TrimSpace(strings.TrimPrefix(l, "webBasePath:"))
		case strings.Contains(l, "secure with SSL") && !strings.Contains(l, "not secure"):
			secure = true
		}
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return port, base, secure
}

func Domain() string {
	db, err := sql.Open("sqlite", "file:"+DB+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return ""
	}
	defer db.Close()
	var v string
	db.QueryRow("SELECT value FROM settings WHERE key = 'webDomain'").Scan(&v)
	return strings.TrimSpace(v)
}

func FallbackToken(ctx context.Context, run sysx.Runner) (string, error) {
	out, err := run.Run(ctx, Binary, "setting", "-getApiToken", "-tokenName", TokenName)
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if _, v, ok := strings.Cut(l, "apiToken:"); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("x-ui did not print an API token")
}

func NewClient(p Panel, a Auth) (*Client, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		p.URL = u.String()
	}
	jar, _ := cookiejar.New(nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	c := &Client{Panel: p, Auth: a, http: &http.Client{Transport: tr, Jar: jar, Timeout: 15 * time.Second}}
	if loopback(u.Hostname()) {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		c.host = Domain()
	}
	return c, nil
}

func loopback(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Panel.URL+path, rd)
	if err != nil {
		return err
	}
	if c.host != "" {
		req.Host = c.host
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Auth.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Auth.Token)
	} else if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("3x-ui: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRespLen))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("3x-ui rejected the credentials (wrong, revoked or disabled token)")
	case http.StatusForbidden:
		return fmt.Errorf("3x-ui refused %s: the token scope does not allow it, or the panel domain does not match", path)
	case http.StatusNotFound:
		return fmt.Errorf("3x-ui returned 404 for %s: wrong basePath or missing authentication", path)
	default:
		return fmt.Errorf("3x-ui %s: %s", path, resp.Status)
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("3x-ui %s: unexpected response", path)
	}
	if !env.Success {
		return fmt.Errorf("3x-ui %s: %s", path, env.Msg)
	}
	if out != nil {
		return json.Unmarshal(env.Obj, out)
	}
	return nil
}

func (c *Client) Login(ctx context.Context) error {
	if c.Auth.Token != "" {
		return nil
	}
	var token string
	if err := c.do(ctx, http.MethodGet, "csrf-token", nil, &token); err == nil {
		c.csrf = token
	}
	form := url.Values{"username": {c.Auth.User}, "password": {c.Auth.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Panel.URL+"login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.host != "" {
		req.Host = c.host
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRespLen)).Decode(&env); err != nil || !env.Success {
		return errors.New("3x-ui login failed")
	}
	return nil
}

type Account struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

type InboundSettings struct {
	Auth     string    `json:"auth"`
	Accounts []Account `json:"accounts"`
	UDP      bool      `json:"udp"`
	IP       string    `json:"ip"`
}

type Sniffing struct {
	Enabled      bool     `json:"enabled"`
	DestOverride []string `json:"destOverride"`
}

type Inbound struct {
	ID             int             `json:"id,omitempty"`
	Up             int64           `json:"up"`
	Down           int64           `json:"down"`
	Total          int64           `json:"total"`
	Remark         string          `json:"remark"`
	Enable         bool            `json:"enable"`
	ExpiryTime     int64           `json:"expiryTime"`
	Listen         string          `json:"listen"`
	Port           int             `json:"port"`
	Protocol       string          `json:"protocol"`
	Settings       json.RawMessage `json:"settings"`
	StreamSettings json.RawMessage `json:"streamSettings"`
	Tag            string          `json:"tag"`
	Sniffing       json.RawMessage `json:"sniffing"`
}

func (in Inbound) Accounts() []Account {
	var s InboundSettings
	json.Unmarshal(object(in.Settings), &s)
	return s.Accounts
}

func object(raw json.RawMessage) []byte {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []byte(str)
	}
	return raw
}

func SocksInbound(port int, user, pass string) Inbound {
	settings, _ := json.Marshal(InboundSettings{Auth: "password", Accounts: []Account{{User: user, Pass: pass}}, UDP: true, IP: "127.0.0.1"})
	sniffing, _ := json.Marshal(Sniffing{Enabled: true, DestOverride: []string{"http", "tls", "quic"}})
	return Inbound{
		Remark:         Tag,
		Enable:         true,
		Listen:         "127.0.0.1",
		Port:           port,
		Protocol:       "mixed",
		Settings:       settings,
		StreamSettings: json.RawMessage(`""`),
		Tag:            Tag,
		Sniffing:       sniffing,
	}
}

func (c *Client) Inbounds(ctx context.Context) ([]Inbound, error) {
	var out []Inbound
	return out, c.do(ctx, http.MethodGet, apiPrefix+"inbounds/list", nil, &out)
}

var ErrNoInbound = errors.New("inbound not found")

func (c *Client) Inbound(ctx context.Context, id int) (Inbound, error) {
	ins, err := c.Inbounds(ctx)
	if err != nil {
		return Inbound{}, err
	}
	for _, in := range ins {
		if in.ID == id {
			return in, nil
		}
	}
	return Inbound{}, fmt.Errorf("%w: id %d", ErrNoInbound, id)
}

func (c *Client) AddInbound(ctx context.Context, in Inbound) (Inbound, error) {
	var out Inbound
	return out, c.do(ctx, http.MethodPost, apiPrefix+"inbounds/add", in, &out)
}

func (c *Client) UpdateInbound(ctx context.Context, id int, in Inbound) error {
	in.ID = id
	return c.do(ctx, http.MethodPost, apiPrefix+"inbounds/update/"+strconv.Itoa(id), in, nil)
}

func (c *Client) DeleteInbound(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, apiPrefix+"inbounds/del/"+strconv.Itoa(id), nil, nil)
}
