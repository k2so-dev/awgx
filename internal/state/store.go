package state

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/secrets"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	Source string
	db     *sql.DB
	box    *secrets.Box
}

func Open(path string, box *secrets.Box) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, box: box}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		os.Chmod(path, 0o600)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		conn.ExecContext(ctx, "ROLLBACK")
		return err
	}
	for i := version; i < len(migrations); i++ {
		if _, err := conn.ExecContext(ctx, migrations[i]); err != nil {
			conn.ExecContext(ctx, "ROLLBACK")
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			conn.ExecContext(ctx, "ROLLBACK")
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) Check() error {
	var result string
	if err := s.db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

func LockContext(ctx context.Context, dir string) (func(), error) {
	return lockFile(ctx, filepath.Join(dir, "state.lock"))
}

func lockFile(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Store) SaveInstance(inst *Instance) error {
	obf, err := json.Marshal(inst.Obfuscation)
	if err != nil {
		return err
	}
	i1s, err := json.Marshal(inst.I1)
	if err != nil {
		return err
	}
	dns, err := json.Marshal(inst.DNS)
	if err != nil {
		return err
	}
	priv, err := s.box.SealKey(inst.PrivateKey)
	if err != nil {
		return err
	}
	hpk, err := s.box.SealKey(inst.Obfuscation.HeaderProtectionKey)
	if err != nil {
		return err
	}
	ts := now()
	if inst.CreatedAt.IsZero() {
		inst.CreatedAt = parseTime(ts)
	}
	inst.UpdatedAt = parseTime(ts)
	_, err = s.db.Exec(`INSERT INTO instances (name, listen_port, endpoint, mtu, subnet4, subnet6, dns, preset,
		obfuscation, i1, i1_rotated_at, private_key, header_protection_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET listen_port = excluded.listen_port, endpoint = excluded.endpoint,
		mtu = excluded.mtu, subnet4 = excluded.subnet4, subnet6 = excluded.subnet6, dns = excluded.dns,
		preset = excluded.preset, obfuscation = excluded.obfuscation, i1 = excluded.i1,
		i1_rotated_at = excluded.i1_rotated_at, private_key = excluded.private_key,
		header_protection_key = excluded.header_protection_key, updated_at = excluded.updated_at`,
		inst.Name, inst.ListenPort, inst.Endpoint, inst.MTU, inst.Subnet4.String(), OrEmpty(inst.Subnet6),
		string(dns), inst.Preset, string(obf), string(i1s), formatTime(inst.I1RotatedAt), priv, hpk,
		formatTime(inst.CreatedAt), formatTime(inst.UpdatedAt))
	return err
}

func (s *Store) Instance(name string) (Instance, error) {
	insts, err := s.queryInstances("WHERE name = ?", name)
	if err != nil {
		return Instance{}, err
	}
	if len(insts) == 0 {
		return Instance{}, fmt.Errorf("instance %s: %w", name, ErrNotFound)
	}
	return insts[0], nil
}

func (s *Store) Instances() ([]Instance, error) {
	return s.queryInstances("ORDER BY name")
}

func (s *Store) queryInstances(where string, args ...any) ([]Instance, error) {
	rows, err := s.db.Query(`SELECT name, listen_port, endpoint, mtu, subnet4, subnet6, dns, preset, obfuscation, i1,
		i1_rotated_at, private_key, header_protection_key, created_at, updated_at FROM instances `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		var inst Instance
		var subnet4, subnet6, dns, obf, i1s, rotated, created, updated string
		var priv, hpk []byte
		if err := rows.Scan(&inst.Name, &inst.ListenPort, &inst.Endpoint, &inst.MTU, &subnet4, &subnet6, &dns,
			&inst.Preset, &obf, &i1s, &rotated, &priv, &hpk, &created, &updated); err != nil {
			return nil, err
		}
		inst.Subnet4, _ = netip.ParsePrefix(subnet4)
		inst.Subnet6, _ = netip.ParsePrefix(subnet6)
		if err := json.Unmarshal([]byte(dns), &inst.DNS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(obf), &inst.Obfuscation); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(i1s), &inst.I1); err != nil {
			return nil, err
		}
		if inst.PrivateKey, err = s.box.OpenKey(priv); err != nil {
			return nil, err
		}
		if inst.Obfuscation.HeaderProtectionKey, err = s.box.OpenKey(hpk); err != nil {
			return nil, err
		}
		inst.I1RotatedAt = parseTime(rotated)
		inst.CreatedAt = parseTime(created)
		inst.UpdatedAt = parseTime(updated)
		out = append(out, inst)
	}
	return out, rows.Err()
}

func (s *Store) DeleteInstance(name string) error {
	_, err := s.db.Exec("DELETE FROM instances WHERE name = ?", name)
	return err
}

func (s *Store) SavePeer(p *Peer) error {
	priv, err := s.box.SealKey(p.PrivateKey)
	if err != nil {
		return err
	}
	psk, err := s.box.SealKey(p.PresharedKey)
	if err != nil {
		return err
	}
	var i1s []byte
	if p.I1 != nil {
		if i1s, err = json.Marshal(p.I1); err != nil {
			return err
		}
	}
	allowed := make([]string, len(p.AllowedIPs))
	for i, a := range p.AllowedIPs {
		allowed[i] = a.String()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = parseTime(now())
	}
	args := []any{p.Instance, p.Name, p.PublicKey.String(), priv, psk, p.Address4.String(), OrEmpty(p.Address6),
		strings.Join(allowed, ","), p.Egress, p.Killswitch, p.Keepalive.String(), string(i1s), p.I1CPS, p.NetLabel,
		p.Enabled, formatTime(p.ExpiresAt), p.Note, formatTime(p.CreatedAt)}
	if p.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO peers (instance, name, public_key, private_key, preshared_key, address4,
			address6, allowed_ips, egress, killswitch, keepalive, i1, i1_cps, net_label, enabled, expires_at, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
		if err != nil {
			return err
		}
		p.ID, err = res.LastInsertId()
		return err
	}
	_, err = s.db.Exec(`UPDATE peers SET instance = ?, name = ?, public_key = ?, private_key = ?, preshared_key = ?,
		address4 = ?, address6 = ?, allowed_ips = ?, egress = ?, killswitch = ?, keepalive = ?, i1 = ?, i1_cps = ?, net_label = ?,
		enabled = ?, expires_at = ?, note = ?, created_at = ? WHERE id = ?`, append(args, p.ID)...)
	return err
}

func (s *Store) Peer(name string) (Peer, error) {
	peers, err := s.queryPeers("WHERE name = ?", name)
	if err != nil {
		return Peer{}, err
	}
	if len(peers) == 0 {
		return Peer{}, fmt.Errorf("peer %s: %w", name, ErrNotFound)
	}
	return peers[0], nil
}

const SettingUpdate = "update_available"

func (s *Store) Peers(instance string) ([]Peer, error) {
	return s.queryPeers("WHERE instance = ? ORDER BY id", instance)
}

func (s *Store) DeletePeer(name string) error {
	_, err := s.db.Exec("DELETE FROM peers WHERE name = ?", name)
	return err
}

func (s *Store) queryPeers(where string, args ...any) ([]Peer, error) {
	rows, err := s.db.Query(`SELECT id, instance, name, public_key, private_key, preshared_key, address4, address6,
		allowed_ips, egress, killswitch, keepalive, i1, i1_cps, net_label, enabled, expires_at, note, created_at FROM peers `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var pub, addr4, addr6, allowed, keepalive, i1s, expires, created string
		var priv, psk []byte
		if err := rows.Scan(&p.ID, &p.Instance, &p.Name, &pub, &priv, &psk, &addr4, &addr6, &allowed, &p.Egress,
			&p.Killswitch, &keepalive, &i1s, &p.I1CPS, &p.NetLabel, &p.Enabled, &expires, &p.Note, &created); err != nil {
			return nil, err
		}
		if p.PublicKey, err = awgconf.ParseKey(pub); err != nil {
			return nil, err
		}
		if p.PrivateKey, err = s.box.OpenKey(priv); err != nil {
			return nil, err
		}
		if p.PresharedKey, err = s.box.OpenKey(psk); err != nil {
			return nil, err
		}
		p.Address4, _ = netip.ParseAddr(addr4)
		p.Address6, _ = netip.ParseAddr(addr6)
		for _, a := range strings.Split(allowed, ",") {
			if pfx, err := netip.ParsePrefix(a); err == nil {
				p.AllowedIPs = append(p.AllowedIPs, pfx)
			}
		}
		p.Keepalive, _ = awgconf.ParseRange(keepalive, 65535)
		if i1s != "" {
			p.I1 = new(i1.Settings)
			if err := json.Unmarshal([]byte(i1s), p.I1); err != nil {
				return nil, err
			}
		}
		p.ExpiresAt = parseTime(expires)
		p.CreatedAt = parseTime(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AddArtifact(path, kind string) error {
	_, err := s.db.Exec("INSERT INTO artifacts (path, kind, created_at) VALUES (?, ?, ?) ON CONFLICT(path) DO NOTHING",
		path, kind, now())
	return err
}

func (s *Store) DeleteArtifact(path string) error {
	_, err := s.db.Exec("DELETE FROM artifacts WHERE path = ?", path)
	return err
}

func (s *Store) Artifacts() ([]Artifact, error) {
	rows, err := s.db.Query("SELECT path, kind, created_at FROM artifacts ORDER BY created_at DESC, path")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var created string
		if err := rows.Scan(&a.Path, &a.Kind, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Log(action, detail string) error {
	_, err := s.db.Exec("INSERT INTO journal (at, action, detail, source) VALUES (?, ?, ?, ?)", now(), action, detail, s.Source)
	return err
}

func (s *Store) Journal(limit int) ([]JournalEntry, error) {
	rows, err := s.db.Query("SELECT at, action, detail, source FROM journal ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var at string
		if err := rows.Scan(&at, &e.Action, &e.Detail, &e.Source); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func secretKey(key string) string {
	return "secret." + key
}

func (s *Store) HasSecret(key string) (bool, error) {
	raw, err := s.Setting(secretKey(key))
	return raw != "", err
}

func (s *Store) DeleteSecret(key string) error {
	return s.DeleteSetting(secretKey(key))
}

func (s *Store) SecretSetting(key string, v any) (bool, error) {
	raw, err := s.Setting(secretKey(key))
	if err != nil || raw == "" {
		return false, err
	}
	sealed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false, err
	}
	return true, s.openJSON(sealed, v)
}

func (s *Store) SetSecretSetting(key string, v any) error {
	sealed, err := s.sealJSON(v)
	if err != nil {
		return err
	}
	return s.SetSetting(secretKey(key), base64.StdEncoding.EncodeToString(sealed))
}

func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec("DELETE FROM settings WHERE key = ?", key)
	return err
}

func (s *Store) Exec(query string, args ...any) (sql.Result, error) {
	return s.db.Exec(query, args...)
}

func OrEmpty(v interface {
	IsValid() bool
	String() string
}) string {
	if !v.IsValid() {
		return ""
	}
	return v.String()
}
