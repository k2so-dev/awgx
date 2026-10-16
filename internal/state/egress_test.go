package state

import (
	"path/filepath"
	"testing"

	"github.com/k2so-dev/awgx/internal/secrets"
)

func TestSnapshotPruningKeepsConfirmed(t *testing.T) {
	box, err := secrets.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(":memory:", box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	keep := Snapshot{Status: SnapshotConfirmed, Config: EgressConfig{Profiles: []EgressProfile{{Name: "up"}}}}
	if err := s.AddSnapshot(&keep); err != nil {
		t.Fatal(err)
	}
	for range 30 {
		sn := Snapshot{Status: SnapshotRolledBack}
		if err := s.AddSnapshot(&sn); err != nil {
			t.Fatal(err)
		}
	}
	applied, err := s.Applied()
	if err != nil {
		t.Fatal(err)
	}
	if applied.ID != keep.ID {
		t.Fatalf("confirmed snapshot was pruned, applied is %d", applied.ID)
	}
	all, _ := s.Snapshots(100)
	if len(all) != 21 {
		t.Fatalf("kept %d snapshots", len(all))
	}
}
