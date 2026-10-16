package state

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const Direct = "direct"

type Hop struct {
	Dev string     `json:"dev"`
	Via netip.Addr `json:"via,omitzero"`
}

type Socks struct {
	Address  string `json:"address"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	NoUDP    bool   `json:"no_udp,omitempty"`
}

type EgressProfile struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Table     int    `json:"table"`
	Hops      []Hop  `json:"hops,omitempty"`
	Socks     *Socks `json:"socks,omitempty"`
	Fallback  string `json:"fallback,omitempty"`
	DNSHijack bool   `json:"dns_hijack,omitempty"`
}

func (p EgressProfile) Relayed() bool {
	return p.Kind == "socks5" || p.Kind == "xui"
}

func (p EgressProfile) Target() string {
	if p.Socks != nil {
		return p.Socks.Address
	}
	var devs []string
	for _, h := range p.Hops {
		d := h.Dev
		if h.Via.IsValid() {
			d += " via " + h.Via.String()
		}
		devs = append(devs, d)
	}
	return strings.Join(devs, ", ")
}

type Assign struct {
	Profile    string `json:"profile"`
	Killswitch bool   `json:"killswitch,omitempty"`
}

type EgressConfig struct {
	Profiles []EgressProfile   `json:"profiles"`
	Default  map[string]Assign `json:"default"`
	Peers    map[string]Assign `json:"peers"`
}

func (c EgressConfig) Profile(name string) (EgressProfile, bool) {
	for _, p := range c.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return EgressProfile{}, false
}

type Snapshot struct {
	ID        int64        `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	Status    string       `json:"status"`
	Config    EgressConfig `json:"-"`
	Base      int64        `json:"base"`
	Admin     string       `json:"admin,omitempty"`
	Diff      string       `json:"diff"`
	Checks    string       `json:"checks"`
}

const (
	SnapshotPending    = "pending"
	SnapshotConfirmed  = "confirmed"
	SnapshotRolledBack = "rolled_back"
	SnapshotFailed     = "failed"
)

type Uplink struct {
	Name      string    `json:"name"`
	Conf      string    `json:"-"`
	Routed    bool      `json:"routed"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) sealJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(b)
}

func (s *Store) openJSON(sealed []byte, v any) error {
	b, err := s.box.Open(sealed)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) SaveProfile(p EgressProfile) error {
	cfg, err := s.sealJSON(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO egress_profiles (name, kind, config, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET kind = excluded.kind, config = excluded.config`, p.Name, p.Kind, cfg, now())
	return err
}

func (s *Store) DeleteProfile(name string) error {
	_, err := s.db.Exec("DELETE FROM egress_profiles WHERE name = ?", name)
	return err
}

func (s *Store) Profiles() ([]EgressProfile, error) {
	rows, err := s.db.Query("SELECT config FROM egress_profiles ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressProfile
	for rows.Next() {
		var sealed []byte
		if err := rows.Scan(&sealed); err != nil {
			return nil, err
		}
		var p EgressProfile
		if err := s.openJSON(sealed, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetInstanceEgress(name string, a Assign) error {
	_, err := s.db.Exec("UPDATE instances SET egress = ?, killswitch = ? WHERE name = ?", a.Profile, a.Killswitch, name)
	return err
}

func (s *Store) SetPeerEgress(name string, a Assign) error {
	_, err := s.db.Exec("UPDATE peers SET egress = ?, killswitch = ? WHERE name = ?", a.Profile, a.Killswitch, name)
	return err
}

func (s *Store) Draft() (EgressConfig, error) {
	c := EgressConfig{Default: map[string]Assign{}, Peers: map[string]Assign{}}
	var err error
	if c.Profiles, err = s.Profiles(); err != nil {
		return c, err
	}
	rows, err := s.db.Query("SELECT name, egress, killswitch FROM instances")
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var name string
		var a Assign
		if err := rows.Scan(&name, &a.Profile, &a.Killswitch); err != nil {
			rows.Close()
			return c, err
		}
		c.Default[name] = a
	}
	rows.Close()
	rows, err = s.db.Query("SELECT name, egress, killswitch FROM peers WHERE egress != '' OR killswitch != 0")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var a Assign
		if err := rows.Scan(&name, &a.Profile, &a.Killswitch); err != nil {
			return c, err
		}
		c.Peers[name] = a
	}
	return c, rows.Err()
}

func (s *Store) RestoreDraft(c EgressConfig) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM egress_profiles"); err != nil {
		return err
	}
	for _, p := range c.Profiles {
		cfg, err := s.sealJSON(p)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO egress_profiles (name, kind, config, created_at) VALUES (?, ?, ?, ?)",
			p.Name, p.Kind, cfg, now()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE instances SET egress = ?, killswitch = 0", Direct); err != nil {
		return err
	}
	for name, a := range c.Default {
		if _, err := tx.Exec("UPDATE instances SET egress = ?, killswitch = ? WHERE name = ?", a.Profile, a.Killswitch, name); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE peers SET egress = '', killswitch = 0"); err != nil {
		return err
	}
	for name, a := range c.Peers {
		if _, err := tx.Exec("UPDATE peers SET egress = ?, killswitch = ? WHERE name = ?", a.Profile, a.Killswitch, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddSnapshot(sn *Snapshot) error {
	cfg, err := s.sealJSON(sn.Config)
	if err != nil {
		return err
	}
	if sn.CreatedAt.IsZero() {
		sn.CreatedAt = parseTime(now())
	}
	res, err := s.db.Exec(`INSERT INTO egress_snapshots (created_at, status, config, base, deadline, admin, diff, checks)
		VALUES (?, ?, ?, ?, '', ?, ?, ?)`, formatTime(sn.CreatedAt), sn.Status, cfg, sn.Base,
		sn.Admin, sn.Diff, sn.Checks)
	if err != nil {
		return err
	}
	sn.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM egress_snapshots WHERE status != ? AND id NOT IN
		(SELECT id FROM egress_snapshots ORDER BY id DESC LIMIT 20)
		AND id != COALESCE((SELECT MAX(id) FROM egress_snapshots WHERE status = ?), 0)`, SnapshotPending, SnapshotConfirmed)
	return err
}

func (s *Store) UpdateSnapshot(id int64, status, checks string) error {
	_, err := s.db.Exec("UPDATE egress_snapshots SET status = ?, checks = ? WHERE id = ?", status, checks, id)
	return err
}

func (s *Store) Snapshot(id int64) (Snapshot, error) {
	sns, err := s.querySnapshots("WHERE id = ?", id)
	if err != nil {
		return Snapshot{}, err
	}
	if len(sns) == 0 {
		return Snapshot{}, fmt.Errorf("snapshot %d: %w", id, ErrNotFound)
	}
	return sns[0], nil
}

func (s *Store) Snapshots(limit int) ([]Snapshot, error) {
	return s.querySnapshots("ORDER BY id DESC LIMIT ?", limit)
}

func (s *Store) Applied() (Snapshot, error) {
	sns, err := s.querySnapshots("WHERE status IN (?, ?) ORDER BY id DESC LIMIT 1", SnapshotPending, SnapshotConfirmed)
	if err != nil {
		return Snapshot{}, err
	}
	if len(sns) == 0 {
		return Snapshot{Config: EgressConfig{Default: map[string]Assign{}, Peers: map[string]Assign{}}}, nil
	}
	return sns[0], nil
}

func (s *Store) Confirmed(before int64) (Snapshot, error) {
	sns, err := s.querySnapshots("WHERE status = ? AND id < ? ORDER BY id DESC LIMIT 1", SnapshotConfirmed, before)
	if err != nil {
		return Snapshot{}, err
	}
	if len(sns) == 0 {
		return Snapshot{Config: EgressConfig{Default: map[string]Assign{}, Peers: map[string]Assign{}}}, nil
	}
	return sns[0], nil
}

func (s *Store) querySnapshots(where string, args ...any) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id, created_at, status, config, base, admin, diff, checks
		FROM egress_snapshots `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var sn Snapshot
		var created string
		var cfg []byte
		if err := rows.Scan(&sn.ID, &created, &sn.Status, &cfg, &sn.Base, &sn.Admin, &sn.Diff, &sn.Checks); err != nil {
			return nil, err
		}
		if err := s.openJSON(cfg, &sn.Config); err != nil {
			return nil, err
		}
		if sn.Config.Default == nil {
			sn.Config.Default = map[string]Assign{}
		}
		if sn.Config.Peers == nil {
			sn.Config.Peers = map[string]Assign{}
		}
		sn.CreatedAt = parseTime(created)
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) SaveUplink(u *Uplink) error {
	conf, err := s.box.Seal([]byte(u.Conf))
	if err != nil {
		return err
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = parseTime(now())
	}
	_, err = s.db.Exec(`INSERT INTO uplinks (name, config, routed, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET config = excluded.config, routed = excluded.routed`,
		u.Name, conf, u.Routed, formatTime(u.CreatedAt))
	return err
}

func (s *Store) DeleteUplink(name string) error {
	_, err := s.db.Exec("DELETE FROM uplinks WHERE name = ?", name)
	return err
}

func (s *Store) Uplink(name string) (Uplink, error) {
	ups, err := s.queryUplinks("WHERE name = ?", name)
	if err != nil {
		return Uplink{}, err
	}
	if len(ups) == 0 {
		return Uplink{}, fmt.Errorf("uplink %s: %w", name, ErrNotFound)
	}
	return ups[0], nil
}

func (s *Store) Uplinks() ([]Uplink, error) {
	return s.queryUplinks("ORDER BY name")
}

func (s *Store) queryUplinks(where string, args ...any) ([]Uplink, error) {
	rows, err := s.db.Query("SELECT name, config, routed, created_at FROM uplinks "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Uplink
	for rows.Next() {
		var u Uplink
		var sealed []byte
		var created string
		if err := rows.Scan(&u.Name, &sealed, &u.Routed, &created); err != nil {
			return nil, err
		}
		conf, err := s.box.Open(sealed)
		if err != nil {
			return nil, err
		}
		u.Conf = string(conf)
		u.CreatedAt = parseTime(created)
		out = append(out, u)
	}
	return out, rows.Err()
}
