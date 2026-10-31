package store

import (
	"strconv"
	"testing"
	"time"
)

func TestProgressStoreSetGetDelete(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")

	if _, err := s.Progress.Get(u.ID, "item:1"); err != ErrProgressNotFound {
		t.Fatalf("empty Get = %v, want ErrProgressNotFound", err)
	}
	if err := s.Progress.Set(u.ID, 1, "item:1", 42, 600); err != nil {
		t.Fatalf("Set: %v", err)
	}
	p, err := s.Progress.Get(u.ID, "item:1")
	if err != nil || p.PositionSec != 42 || p.DurationSec != 600 || p.ItemID != 1 {
		t.Fatalf("Get = %+v, %v", p, err)
	}
	// A later write updates the same row and its time, not a second row.
	if err := s.Progress.Set(u.ID, 1, "item:1", 84, 600); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	p, _ = s.Progress.Get(u.ID, "item:1")
	if p.PositionSec != 84 {
		t.Fatalf("updated position = %d, want 84", p.PositionSec)
	}
	if err := s.Progress.Delete(u.ID, "item:1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Progress.Get(u.ID, "item:1"); err != ErrProgressNotFound {
		t.Fatalf("Get after delete = %v, want ErrProgressNotFound", err)
	}
}

func TestProgressStoreTrimsPerUser(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	for i := 0; i < MaxProgressPerUser+10; i++ {
		if err := s.Progress.Set(u.ID, int64(i+1), "plugin:youtube:v"+string(rune('a'+i%26))+strconv.Itoa(i), 10, 100); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM item_progress WHERE user_id = ?`, u.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != MaxProgressPerUser {
		t.Fatalf("rows = %d, want %d", n, MaxProgressPerUser)
	}
}

func TestProgressStoreSweepsOldRows(t *testing.T) {
	s := newTestStore(t)
	u := mustUser(t, s, "alice")
	if err := s.Progress.Set(u.ID, 1, "item:1", 10, 100); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Age the row past the cutoff.
	if _, err := s.db.Exec(`UPDATE item_progress SET updated_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatalf("age row: %v", err)
	}
	n, err := s.Progress.DeleteOlderThan(time.Now().Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v", n, err)
	}
	if _, err := s.Progress.Get(u.ID, "item:1"); err != ErrProgressNotFound {
		t.Fatalf("aged row should be gone, got %v", err)
	}
}
