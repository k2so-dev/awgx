package webconf

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"github.com/k2so-dev/awgx/internal/core"
	"golang.org/x/crypto/argon2"
)

const (
	DefaultListen = "127.0.0.1:7457"
	MinPassword   = 10
	KeyEnabled    = "web.enabled"
	KeyListen     = "web.listen"
	KeyPassword   = "web.password"
	KeyMetrics    = "web.metrics"
	KeyHost       = "web.host"
	SecretTOTP    = "web.totp"
	argonTime     = 3
	argonMemory   = 64 * 1024
	argonThreads  = 2
	argonKeyLen   = 32
)

type Config struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	Metrics bool   `json:"metrics"`
	Host    string `json:"host,omitempty"`
	HasPass bool   `json:"has_password"`
	TOTP    bool   `json:"totp"`
}

func LoadConfig(app *core.App) (Config, error) {
	c := Config{Listen: DefaultListen}
	s, err := app.Store()
	if err != nil {
		return c, err
	}
	v, _ := s.Setting(KeyEnabled)
	c.Enabled = v == "1"
	if v, _ := s.Setting(KeyListen); v != "" {
		c.Listen = v
	}
	v, _ = s.Setting(KeyMetrics)
	c.Metrics = v == "1"
	c.Host, _ = s.Setting(KeyHost)
	v, _ = s.Setting(KeyPassword)
	c.HasPass = v != ""
	c.TOTP, _ = s.HasSecret(SecretTOTP)
	return c, nil
}

func SetMetrics(app *core.App, on bool) error {
	s, err := app.Store()
	if err != nil {
		return err
	}
	v := "0"
	if on {
		v = "1"
	}
	return s.SetSetting(KeyMetrics, v)
}

func SetPassword(app *core.App, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	s, err := app.Store()
	if err != nil {
		return err
	}
	if err := s.SetSetting(KeyPassword, hash); err != nil {
		return err
	}
	return s.Log("web", "password changed")
}

func Enable(app *core.App, listen, host string) error {
	s, err := app.Store()
	if err != nil {
		return err
	}
	for k, v := range map[string]string{KeyHost: host, KeyListen: listen, KeyEnabled: "1"} {
		if err := s.SetSetting(k, v); err != nil {
			return err
		}
	}
	return s.Log("web", "enabled on "+listen)
}

func Disable(app *core.App) error {
	s, err := app.Store()
	if err != nil {
		return err
	}
	if err := s.SetSetting(KeyEnabled, "0"); err != nil {
		return err
	}
	return s.Log("web", "disabled")
}

func CheckPassword(password string) error {
	if len(password) < MinPassword {
		return fmt.Errorf("password must have at least %d characters", MinPassword)
	}
	return nil
}

func HashPassword(password string) (string, error) {
	if err := CheckPassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func Tunnel(listen, endpoint string) string {
	host, port, _ := net.SplitHostPort(listen)
	server := "SERVER"
	if endpoint != "" {
		server = endpoint
		if h, _, err := net.SplitHostPort(endpoint); err == nil {
			server = h
		}
	}
	return fmt.Sprintf("ssh -N -L %s:%s root@%s", port, net.JoinHostPort(host, port), server)
}

func Endpoint(app *core.App) string {
	inst, err := app.Current()
	if err != nil {
		return ""
	}
	return inst.Endpoint
}
