package stats

import (
	"slices"
	"sort"
	"time"

	"github.com/k2so-dev/awgx/internal/state"
)

type ProfileSummary struct {
	Profile         string        `json:"profile"`
	Peers           int           `json:"peers"`
	AlivePeers      int           `json:"alive_peers"`
	AliveShare      float64       `json:"alive_share"`
	Sessions        int           `json:"sessions"`
	MedianSession   time.Duration `json:"median_session"`
	HandshakesPerPH float64       `json:"handshakes_per_peer_hour"`
	Failures        int           `json:"failures"`
}

type PeerInfo struct {
	ID       int64
	Profile  string
	NetLabel string
}

func ByI1(peers []PeerInfo, samples []state.Sample, sessions []state.Session, since, now time.Time, netLabel string) []ProfileSummary {
	hours := now.Sub(since).Hours()
	groups := map[string]*ProfileSummary{}
	profileOf := map[int64]string{}
	durations := map[string][]time.Duration{}
	for _, p := range peers {
		if netLabel != "" && p.NetLabel != netLabel {
			continue
		}
		g, ok := groups[p.Profile]
		if !ok {
			g = &ProfileSummary{Profile: p.Profile}
			groups[p.Profile] = g
		}
		g.Peers++
		profileOf[p.ID] = p.Profile
	}
	alive := map[int64]bool{}
	handshakes := map[string]int{}
	for _, s := range samples {
		profile, ok := profileOf[s.PeerID]
		if !ok {
			continue
		}
		if s.Handshakes > 0 {
			alive[s.PeerID] = true
		}
		handshakes[profile] += s.Handshakes
		groups[profile].Failures += s.Failures
	}
	for id := range alive {
		groups[profileOf[id]].AlivePeers++
	}
	for _, s := range sessions {
		if _, mine := profileOf[s.PeerID]; !mine || netLabel != "" && s.NetLabel != netLabel {
			continue
		}
		g, ok := groups[s.I1Profile]
		if !ok {
			continue
		}
		g.Sessions++
		durations[s.I1Profile] = append(durations[s.I1Profile], s.End.Sub(s.Start))
	}
	out := make([]ProfileSummary, 0, len(groups))
	for name, g := range groups {
		if g.Peers > 0 {
			g.AliveShare = float64(g.AlivePeers) / float64(g.Peers)
			if hours > 0 {
				g.HandshakesPerPH = float64(handshakes[name]) / float64(g.Peers) / hours
			}
		}
		g.MedianSession = median(durations[name])
		out = append(out, *g)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Profile < out[b].Profile })
	return out
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
