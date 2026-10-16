package core

import (
	"context"
	"time"

	"github.com/k2so-dev/awgx/internal/awg"
	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/doctor"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/sysx"
)

type PeerRow struct {
	state.Peer
	Status        string    `json:"status"`
	Online        bool      `json:"online"`
	I1Profile     string    `json:"i1_profile"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	RxBytes       uint64    `json:"rx_bytes"`
	TxBytes       uint64    `json:"tx_bytes"`
	Endpoint      string    `json:"endpoint,omitempty"`
}

const onlineWindow = 3 * time.Minute

func (r PeerRow) Label() string {
	if r.Online {
		return "online"
	}
	return r.Status
}

func (a *App) PeerRows(ctx context.Context, instance string) ([]PeerRow, error) {
	insts, err := a.Instances()
	if err != nil {
		return nil, err
	}
	var rows []PeerRow
	for _, inst := range insts {
		if instance != "" && inst.Name != instance {
			continue
		}
		r, err := a.instancePeerRows(ctx, inst)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	if instance != "" && rows == nil {
		if _, err := a.Instance(instance); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (a *App) instancePeerRows(ctx context.Context, inst state.Instance) ([]PeerRow, error) {
	store, _ := a.Store()
	peers, err := store.Peers(inst.Name)
	if err != nil {
		return nil, err
	}
	live := map[awgconf.Key]awg.PeerStatus{}
	if a.InterfaceUp(inst.Name) {
		if dev, err := a.Tool().Show(ctx, inst.Name); err == nil {
			for _, p := range dev.Peers {
				live[p.PublicKey] = p
			}
		}
	}
	now := time.Now()
	rows := make([]PeerRow, len(peers))
	for i, p := range peers {
		l := live[p.PublicKey]
		status := "enabled"
		switch {
		case p.Expired(now):
			status = "expired"
		case !p.Enabled:
			status = "disabled"
		}
		online := status == "enabled" && !l.LastHandshake.IsZero() && now.Sub(l.LastHandshake) < onlineWindow
		rows[i] = PeerRow{Peer: p, Status: status, Online: online, I1Profile: p.I1Profile(inst), LastHandshake: l.LastHandshake,
			RxBytes: l.RxBytes, TxBytes: l.TxBytes, Endpoint: l.Endpoint}
	}
	return rows, nil
}

func (a *App) DoctorEnv() *doctor.Env {
	env := &doctor.Env{Run: a.Run, OS: sysx.ReadOS(), Kernel: sysx.KernelRelease(), SysctlDir: a.Paths.SysctlDir, Failed: a.FailedKernels()}
	env.Reconcile = func(ctx context.Context, name string) error {
		return a.Locked(func() error {
			_, err := a.Reconcile(ctx, name, true)
			return err
		})
	}
	env.StartServe = a.StartServe
	env.ApplyRate = func(ctx context.Context, name string) error {
		return a.Locked(func() error { return a.ApplyRate(ctx, name) })
	}
	if s, err := a.Store(); err == nil {
		env.Store = s
		if inst, err := a.Current(); err == nil {
			env.Instance = &inst
		}
		env.Instances, _ = s.Instances()
	}
	return env
}
