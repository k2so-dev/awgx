package state

import (
	"time"
)

const (
	BucketRaw  = 10
	Bucket5m   = 300
	Bucket1h   = 3600
	Retention  = 90 * 24 * time.Hour
	rawKeep    = time.Hour
	fiveMiKeep = 24 * time.Hour
)

type Sample struct {
	PeerID        int64     `json:"-"`
	Peer          string    `json:"peer,omitempty"`
	At            time.Time `json:"at"`
	Rx            uint64    `json:"rx"`
	Tx            uint64    `json:"tx"`
	Handshakes    int       `json:"handshakes"`
	Failures      int       `json:"failures"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	Endpoint      string    `json:"endpoint,omitempty"`
}

func (s *Store) AddSamples(samples []Sample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, x := range samples {
		_, err := tx.Exec(`INSERT INTO traffic (peer_id, bucket, ts, rx, tx, handshakes, failures, last_handshake, endpoint)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			x.PeerID, BucketRaw, x.At.Unix(), x.Rx, x.Tx, x.Handshakes, x.Failures, unix(x.LastHandshake), x.Endpoint)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Rollup(now time.Time) error {
	steps := []struct {
		from, to int
		keep     time.Duration
	}{
		{BucketRaw, Bucket5m, rawKeep},
		{Bucket5m, Bucket1h, fiveMiKeep},
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for _, st := range steps {
		cutoff := now.Add(-st.keep).Unix() / int64(st.to) * int64(st.to)
		_, err := tx.Exec(`INSERT INTO traffic (peer_id, bucket, ts, rx, tx, handshakes, failures, last_handshake, endpoint)
			SELECT peer_id, ?, ts / ? * ?, SUM(rx), SUM(tx), SUM(handshakes), SUM(failures), MAX(last_handshake),
			COALESCE((SELECT x.endpoint FROM traffic x WHERE x.peer_id = t.peer_id AND x.bucket = t.bucket
				AND x.ts >= t.ts / ? * ? AND x.ts < MIN(t.ts / ? * ? + ?, ?) AND x.endpoint != '' ORDER BY x.ts DESC LIMIT 1), '')
			FROM traffic t WHERE bucket = ? AND ts < ? GROUP BY peer_id, ts / ?
			ON CONFLICT (peer_id, bucket, ts) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx,
			handshakes = handshakes + excluded.handshakes, failures = failures + excluded.failures,
			last_handshake = MAX(last_handshake, excluded.last_handshake), endpoint = COALESCE(NULLIF(excluded.endpoint, ''), endpoint)`,
			st.to, st.to, st.to, st.to, st.to, st.to, st.to, st.to, cutoff, st.from, cutoff, st.to)
		if err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec("DELETE FROM traffic WHERE bucket = ? AND ts < ?", st.from, cutoff); err != nil {
			tx.Rollback()
			return err
		}
	}
	old := now.Add(-Retention).Unix()
	if _, err := tx.Exec("DELETE FROM traffic WHERE ts < ?", old); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE open = 0 AND ended_at < ?", old); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Traffic(f TrafficFilter, since time.Time) ([]Sample, error) {
	q := `SELECT t.peer_id, p.name, t.ts, t.rx, t.tx, t.handshakes, t.failures, t.last_handshake, t.endpoint
		FROM traffic t JOIN peers p ON p.id = t.peer_id WHERE t.ts >= ?`
	where, args := f.sql("t.peer_id")
	args = append([]any{since.Unix()}, args...)
	rows, err := s.db.Query(q+where+" ORDER BY t.ts", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var x Sample
		var ts, hs int64
		if err := rows.Scan(&x.PeerID, &x.Peer, &ts, &x.Rx, &x.Tx, &x.Handshakes, &x.Failures, &hs, &x.Endpoint); err != nil {
			return nil, err
		}
		x.At = time.Unix(ts, 0)
		x.LastHandshake = fromUnix(hs)
		out = append(out, x)
	}
	return out, rows.Err()
}

type PeerTotal struct {
	Peer          string    `json:"peer"`
	Rx            uint64    `json:"rx"`
	Tx            uint64    `json:"tx"`
	Handshakes    int       `json:"handshakes"`
	Failures      int       `json:"failures"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	Endpoint      string    `json:"endpoint,omitempty"`
}

type TrafficFilter struct {
	Peer     int64
	Instance string
	Net      string
}

func (f TrafficFilter) sql(col string) (string, []any) {
	var q string
	var args []any
	if f.Peer != 0 {
		q += " AND " + col + " = ?"
		args = append(args, f.Peer)
	}
	if f.Instance != "" {
		q += " AND " + col + " IN (SELECT id FROM peers WHERE instance = ?)"
		args = append(args, f.Instance)
	}
	if f.Net != "" {
		q += " AND " + col + " IN (SELECT id FROM peers WHERE net_label = ?)"
		args = append(args, f.Net)
	}
	return q, args
}

func (s *Store) TrafficTotals(f TrafficFilter, since time.Time) ([]PeerTotal, error) {
	q := `SELECT p.name, SUM(t.rx), SUM(t.tx), SUM(t.handshakes), SUM(t.failures), MAX(t.last_handshake),
		COALESCE((SELECT e.endpoint FROM traffic e WHERE e.peer_id = t.peer_id AND e.ts >= ? AND e.endpoint != '' ORDER BY e.ts DESC LIMIT 1), '')
		FROM traffic t JOIN peers p ON p.id = t.peer_id WHERE t.ts >= ?`
	where, args := f.sql("t.peer_id")
	args = append([]any{since.Unix(), since.Unix()}, args...)
	rows, err := s.db.Query(q+where+" GROUP BY t.peer_id ORDER BY SUM(t.rx) + SUM(t.tx) DESC, p.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PeerTotal{}
	for rows.Next() {
		var t PeerTotal
		var hs int64
		if err := rows.Scan(&t.Peer, &t.Rx, &t.Tx, &t.Handshakes, &t.Failures, &hs, &t.Endpoint); err != nil {
			return nil, err
		}
		t.LastHandshake = fromUnix(hs)
		out = append(out, t)
	}
	return out, rows.Err()
}

type Point struct {
	At int64  `json:"at"`
	Rx uint64 `json:"rx"`
	Tx uint64 `json:"tx"`
}

func (s *Store) TrafficSeries(f TrafficFilter, since, until time.Time, n int) ([]Point, error) {
	step := max(1, int64(until.Sub(since).Seconds())/int64(n))
	out := make([]Point, n)
	for i := range out {
		out[i].At = since.Unix() + int64(i)*step
	}
	where, args := f.sql("peer_id")
	args = append([]any{since.Unix(), step, since.Unix()}, args...)
	rows, err := s.db.Query("SELECT (ts - ?) / ?, SUM(rx), SUM(tx) FROM traffic WHERE ts >= ?"+where+" GROUP BY 1", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		var rx, tx uint64
		if err := rows.Scan(&i, &rx, &tx); err != nil {
			return nil, err
		}
		if i >= 0 && i < n {
			out[i].Rx, out[i].Tx = rx, tx
		}
	}
	return out, rows.Err()
}

type Session struct {
	PeerID    int64     `json:"-"`
	I1Profile string    `json:"i1_profile"`
	NetLabel  string    `json:"net_label"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Open      bool      `json:"open"`
}

func (s *Store) OpenSession(peerID int64, profile, label string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (peer_id, i1_profile, net_label, started_at, ended_at, open) VALUES (?, ?, ?, ?, ?, 1)`,
		peerID, profile, label, at.Unix(), at.Unix())
	return err
}

func (s *Store) TouchSession(peerID int64, at time.Time) (bool, error) {
	res, err := s.db.Exec("UPDATE sessions SET ended_at = ? WHERE peer_id = ? AND open = 1", at.Unix(), peerID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) CloseSession(peerID int64) error {
	_, err := s.db.Exec("UPDATE sessions SET open = 0 WHERE peer_id = ? AND open = 1", peerID)
	return err
}

func (s *Store) Sessions(f TrafficFilter, since time.Time) ([]Session, error) {
	where, args := f.sql("peer_id")
	rows, err := s.db.Query(`SELECT peer_id, i1_profile, net_label, started_at, ended_at, open FROM sessions
		WHERE ended_at >= ?`+where+` ORDER BY started_at`, append([]any{since.Unix()}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		var start, end int64
		if err := rows.Scan(&x.PeerID, &x.I1Profile, &x.NetLabel, &start, &end, &x.Open); err != nil {
			return nil, err
		}
		x.Start, x.End = time.Unix(start, 0), time.Unix(end, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
