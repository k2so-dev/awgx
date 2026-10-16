package xui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/k2so-dev/awgx/internal/egress"
	"github.com/k2so-dev/awgx/internal/state"
)

const (
	recordKey   = "xui"
	ProfileName = "xui"
)

type Record struct {
	Panel     Panel  `json:"panel"`
	Auth      Auth   `json:"auth"`
	InboundID int    `json:"inbound_id"`
	Port      int    `json:"port"`
	User      string `json:"user,omitempty"`
	Pass      string `json:"pass,omitempty"`
	Remove    bool   `json:"remove,omitempty"`
	Keep      bool   `json:"keep,omitempty"`
	Since     int64  `json:"since,omitempty"`
}

func Load(s *state.Store) (Record, bool, error) {
	var r Record
	ok, err := s.SecretSetting(recordKey, &r)
	return r, ok, err
}

func (r Record) Client(ctx context.Context) (*Client, error) {
	c, err := NewClient(r.Panel, r.Auth)
	if err != nil {
		return nil, err
	}
	return c, c.Login(ctx)
}

type ConnectOptions struct {
	Panel      string
	Auth       Auth
	Peers      []string
	Instance   string
	Killswitch bool
	Fallback   string
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func freePort(used map[int]bool) (int, error) {
	for range 200 {
		n, err := rand.Int(rand.Reader, big.NewInt(40000))
		if err != nil {
			return 0, err
		}
		p := 20000 + int(n.Int64())
		if used[p] {
			continue
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		l.Close()
		u, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		u.Close()
		return p, nil
	}
	return 0, errors.New("no free local port for the 3x-ui inbound")
}

func Connect(ctx context.Context, e *egress.Engine, o ConnectOptions) (Record, []string, error) {
	var notes []string
	rec, ok, err := Load(e.Store)
	if err != nil {
		return rec, nil, err
	}
	panel, err := Discover(ctx, e.App.Run)
	if o.Panel != "" {
		if err := CheckPanelURL(o.Panel); err != nil {
			return rec, nil, err
		}
		if panel.Version == "" {
			if panel.Version, err = Version(ctx, e.App.Run); err != nil {
				return rec, nil, err
			}
		}
		if err := CheckVersion(panel.Version); err != nil {
			return rec, nil, err
		}
		panel.URL, err = o.Panel, nil
	}
	if err != nil {
		return rec, nil, err
	}
	if o.Auth.Token == "" && o.Auth.User == "" && ok {
		if rec.Panel.URL != panel.URL {
			return rec, nil, fmt.Errorf("stored credentials belong to %s, give an API token for %s", rec.Panel.URL, panel.URL)
		}
		o.Auth = rec.Auth
	}
	c, err := NewClient(panel, o.Auth)
	if err != nil {
		return rec, nil, err
	}
	if err := c.Login(ctx); err != nil {
		return rec, nil, err
	}
	inbounds, err := c.Inbounds(ctx)
	if err != nil {
		return rec, nil, err
	}
	used := map[int]bool{}
	var mine *Inbound
	for i, in := range inbounds {
		used[in.Port] = true
		if ok && in.ID == rec.InboundID {
			mine = &inbounds[i]
		} else if in.Tag == Tag {
			return rec, nil, fmt.Errorf("3x-ui already has an inbound tagged %s (id %d) that awgx does not own", Tag, in.ID)
		}
	}
	rec.Panel, rec.Auth = c.Panel, o.Auth
	if rec.User == "" || rec.Pass == "" {
		rec.User, rec.Pass = "awgx-"+randomString(8), randomString(24)
	}
	if mine == nil {
		rec.Port, err = freePort(used)
		if err != nil {
			return rec, nil, err
		}
		in, err := c.AddInbound(ctx, SocksInbound(rec.Port, rec.User, rec.Pass))
		if err != nil {
			return rec, nil, err
		}
		rec.InboundID = in.ID
		notes = append(notes, fmt.Sprintf("created 3x-ui inbound %d (%s, SOCKS5 on 127.0.0.1:%d, TCP and UDP)", in.ID, Tag, rec.Port))
	} else {
		rec.Port = mine.Port
		if !rec.Matches(*mine) {
			if err := c.UpdateInbound(ctx, mine.ID, SocksInbound(mine.Port, rec.User, rec.Pass)); err != nil {
				return rec, nil, err
			}
			notes = append(notes, fmt.Sprintf("reset 3x-ui inbound %d to SOCKS5 on 127.0.0.1:%d", mine.ID, mine.Port))
		} else {
			notes = append(notes, fmt.Sprintf("reusing 3x-ui inbound %d on 127.0.0.1:%d", mine.ID, mine.Port))
		}
	}
	rec.Remove, rec.Keep, rec.Since = false, false, 0
	if err := e.Store.SetSecretSetting(recordKey, rec); err != nil {
		return rec, notes, err
	}
	if err := waitLocalOnly(ctx, e, rec.Port); err != nil {
		return rec, notes, err
	}
	socks := rec.Socks()
	draft, err := e.Store.Draft()
	if err != nil {
		return rec, notes, err
	}
	if o.Fallback == "" && !o.Killswitch {
		o.Fallback = state.Direct
	}
	if _, exists := draft.Profile(ProfileName); exists {
		_, err = e.UpdateProfile(ctx, ProfileName, func(p *state.EgressProfile) error {
			p.Socks = socks
			switch {
			case o.Fallback != "":
				p.Fallback = o.Fallback
			case o.Killswitch && p.Fallback == state.Direct:
				p.Fallback = ""
			}
			return nil
		})
	} else {
		_, err = e.AddProfile(ctx, state.EgressProfile{Name: ProfileName, Kind: "xui", Socks: socks, DNSHijack: true, Fallback: o.Fallback})
	}
	if err != nil {
		return rec, notes, err
	}
	if err := e.Assign(o.Instance, o.Peers, state.Assign{Profile: ProfileName, Killswitch: o.Killswitch}); err != nil {
		return rec, notes, err
	}
	notes = append(notes, "suggested Xray routing rule in 3x-ui: inboundTag "+Tag+" + port 53 -> dns-out, so names resolve with the 3x-ui DNS settings")
	return rec, notes, nil
}

func (r Record) Socks() *state.Socks {
	return &state.Socks{Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port)), User: r.User, Password: r.Pass}
}

func waitLocalOnly(ctx context.Context, e *egress.Engine, port int) error {
	for range 50 {
		out, err := e.App.Run.Run(ctx, "ss", "-Hltn", "sport = :"+strconv.Itoa(port))
		if err == nil && strings.TrimSpace(string(out)) != "" {
			for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				f := strings.Fields(l)
				if len(f) < 4 {
					continue
				}
				host, _, err := net.SplitHostPort(f[3])
				if err != nil {
					continue
				}
				a, err := netip.ParseAddr(strings.Trim(host, "[]"))
				if err != nil || !a.IsLoopback() {
					return fmt.Errorf("SOCKS5 inbound port %d listens on %s, not only on loopback", port, f[3])
				}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("3x-ui did not start the inbound on 127.0.0.1:%d", port)
}

type Status struct {
	Connected bool   `json:"connected"`
	Panel     string `json:"panel,omitempty"`
	Version   string `json:"version,omitempty"`
	Running   bool   `json:"running"`
	InboundID int    `json:"inbound_id,omitempty"`
	Present   bool   `json:"present"`
	Enabled   bool   `json:"enabled"`
	Matches   bool   `json:"matches"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	Applied   bool   `json:"applied"`
	Untested  bool   `json:"untested,omitempty"`
	Removing  bool   `json:"removing,omitempty"`
	Keeping   bool   `json:"keeping,omitempty"`
	Problem   string `json:"problem,omitempty"`
}

func Check(ctx context.Context, e *egress.Engine) (Status, error) {
	var st Status
	rec, ok, err := Load(e.Store)
	if err != nil || !ok {
		return st, err
	}
	st.Connected, st.Panel, st.InboundID = true, rec.Panel.URL, rec.InboundID
	st.Removing, st.Keeping = rec.Remove, rec.Remove && rec.Keep
	st.Version, _ = Version(ctx, e.App.Run)
	_, aerr := e.App.Run.Run(ctx, "systemctl", "is-active", "--quiet", Unit)
	st.Running = aerr == nil
	if applied, err := e.Store.Applied(); err == nil {
		_, st.Applied = applied.Config.Profile(ProfileName)
	}
	problem := func(s string) (Status, error) {
		st.Problem = s
		return st, nil
	}
	if !st.Running {
		return problem("x-ui service is not running")
	}
	if err := CheckVersion(st.Version); err != nil {
		st.Untested = errors.Is(err, ErrUntested)
		return problem(err.Error())
	}
	c, err := rec.Client(ctx)
	if err != nil {
		return problem(err.Error())
	}
	in, err := c.Inbound(ctx, rec.InboundID)
	if errors.Is(err, ErrNoInbound) {
		return problem("inbound is gone: " + err.Error())
	}
	if err != nil {
		return problem(err.Error())
	}
	st.Present, st.Enabled, st.Up, st.Down = true, in.Enable, in.Up, in.Down
	st.Matches = rec.Matches(in)
	switch {
	case !st.Enabled:
		return problem("inbound is disabled in 3x-ui")
	case !st.Matches:
		return problem("inbound settings were changed in 3x-ui")
	}
	return st, nil
}

func (r Record) Matches(in Inbound) bool {
	acc := in.Accounts()
	return in.Port == r.Port && in.Listen == "127.0.0.1" && in.Protocol == "mixed" &&
		len(acc) == 1 && acc[0].User == r.User && acc[0].Pass == r.Pass
}

func Watch(ctx context.Context, e *egress.Engine, p state.EgressProfile) error {
	if p.Kind != "xui" {
		return nil
	}
	st, err := Check(ctx, e)
	if err != nil {
		return err
	}
	if st.Problem != "" && !st.Untested {
		return errors.New(st.Problem)
	}
	return nil
}

func Detach(e *egress.Engine) (bool, error) {
	draft, err := e.Store.Draft()
	if err != nil {
		return false, err
	}
	if _, ok := draft.Profile(ProfileName); !ok {
		return false, nil
	}
	for inst, a := range draft.Default {
		if a.Profile == ProfileName {
			if err := e.Store.SetInstanceEgress(inst, state.Assign{Profile: state.Direct}); err != nil {
				return false, err
			}
		}
	}
	for peer, a := range draft.Peers {
		if a.Profile == ProfileName {
			if err := e.Store.SetPeerEgress(peer, state.Assign{Profile: state.Direct}); err != nil {
				return false, err
			}
		}
	}
	for _, p := range draft.Profiles {
		if p.Fallback == ProfileName {
			if _, err := e.UpdateProfile(context.Background(), p.Name, func(p *state.EgressProfile) error { p.Fallback = ""; return nil }); err != nil {
				return false, err
			}
		}
	}
	return true, e.RemoveProfile(ProfileName)
}

func RemoveInbound(ctx context.Context, e *egress.Engine) error {
	applied, err := e.Store.Applied()
	if err != nil {
		return err
	}
	if _, ok := applied.Config.Profile(ProfileName); ok {
		return errors.New("the xui egress is still applied; apply the detach first")
	}
	rec, ok, err := Load(e.Store)
	if err != nil || !ok {
		return err
	}
	c, err := rec.Client(ctx)
	if err != nil {
		return err
	}
	in, err := c.Inbound(ctx, rec.InboundID)
	if err != nil && !errors.Is(err, ErrNoInbound) {
		return err
	}
	if err == nil {
		if rec.Keep {
			in.Listen = "127.0.0.1"
			if err := c.UpdateInbound(ctx, rec.InboundID, in); err != nil {
				return err
			}
			e.Store.Log("xui-disconnect", fmt.Sprintf("inbound %d kept in 3x-ui, now on 127.0.0.1", rec.InboundID))
		} else {
			if err := c.DeleteInbound(ctx, rec.InboundID); err != nil {
				return err
			}
			e.Store.Log("xui-disconnect", fmt.Sprintf("inbound %d removed", rec.InboundID))
		}
	}
	return e.Store.DeleteSecret(recordKey)
}

func Disconnect(e *egress.Engine, keepInbound bool) (bool, error) {
	rec, ok, err := Load(e.Store)
	if err != nil {
		return false, err
	}
	applied, err := e.Store.Applied()
	if err != nil {
		return false, err
	}
	detached, err := Detach(e)
	if err != nil || !ok {
		return detached, err
	}
	rec.Remove, rec.Keep, rec.Since = true, keepInbound, 0
	if _, used := applied.Config.Profile(ProfileName); used {
		rec.Since = applied.ID
		if sns, err := e.Store.Snapshots(1); err == nil && len(sns) > 0 {
			rec.Since = max(rec.Since, sns[0].ID)
		}
	}
	return detached, e.Store.SetSecretSetting(recordKey, rec)
}

func init() {
	egress.AfterApply = AfterApply
}

func AfterApply(ctx context.Context, e *egress.Engine) error {
	rec, ok, err := Load(e.Store)
	if err != nil || !ok || !rec.Remove {
		return err
	}
	applied, err := e.Store.Applied()
	if err != nil {
		return err
	}
	_, used := applied.Config.Profile(ProfileName)
	if !used && (rec.Since == 0 || applied.ID > rec.Since) {
		return RemoveInbound(ctx, e)
	}
	if applied.ID > rec.Since {
		rec.Remove, rec.Keep, rec.Since = false, false, 0
		return e.Store.SetSecretSetting(recordKey, rec)
	}
	sns, err := e.Store.Snapshots(20)
	if err != nil {
		return err
	}
	for _, sn := range sns {
		if sn.ID > rec.Since && (sn.Status == state.SnapshotRolledBack || sn.Status == state.SnapshotFailed) {
			if err := reattach(e, applied.Config); err != nil {
				return err
			}
			rec.Remove, rec.Keep, rec.Since = false, false, 0
			return e.Store.SetSecretSetting(recordKey, rec)
		}
	}
	return nil
}

func reattach(e *egress.Engine, applied state.EgressConfig) error {
	p, ok := applied.Profile(ProfileName)
	if !ok {
		return nil
	}
	draft, err := e.Store.Draft()
	if err != nil {
		return err
	}
	if _, exists := draft.Profile(ProfileName); !exists {
		if err := e.Store.SaveProfile(p); err != nil {
			return err
		}
	}
	for inst, a := range applied.Default {
		if a.Profile == ProfileName {
			if err := e.Store.SetInstanceEgress(inst, a); err != nil {
				return err
			}
		}
	}
	for peer, a := range applied.Peers {
		if a.Profile == ProfileName {
			if err := e.Store.SetPeerEgress(peer, a); err != nil {
				return err
			}
		}
	}
	for _, q := range applied.Profiles {
		if q.Fallback == ProfileName {
			if dq, ok := draft.Profile(q.Name); ok && dq.Fallback == "" {
				dq.Fallback = ProfileName
				if err := e.Store.SaveProfile(dq); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func Maintain(ctx context.Context, e *egress.Engine) (string, error) {
	return "", AfterApply(ctx, e)
}

func Disown(ctx context.Context, e *egress.Engine) error {
	rec, ok, err := Load(e.Store)
	if err != nil || !ok {
		return err
	}
	c, err := rec.Client(ctx)
	if err != nil {
		return err
	}
	if _, err := c.Inbound(ctx, rec.InboundID); errors.Is(err, ErrNoInbound) {
		return nil
	} else if err != nil {
		return err
	}
	return c.DeleteInbound(ctx, rec.InboundID)
}
