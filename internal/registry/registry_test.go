package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFirstRegistrationStoresOwnSourceThenAssignmentWins(t *testing.T) {
	r, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
	d, err := r.Register("tally-1", "cam1", "1.0.0", now)
	if err != nil || d.SourceID != "cam1" {
		t.Fatalf("first registration = %+v, %v", d, err)
	}
	if _, err := r.Assign("tally-1", "cam2"); err != nil {
		t.Fatal(err)
	}
	// The device still has cam1 in its own configuration and registers with
	// it after every reconnect.
	d, err = r.Register("tally-1", "cam1", "1.0.1", now.Add(time.Minute))
	if err != nil || d.SourceID != "cam2" || d.Firmware != "1.0.1" || !d.LastSeen.Equal(now.Add(time.Minute)) {
		t.Fatalf("re-registration = %+v, %v", d, err)
	}
}

func TestSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := Open(dir)
	now := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
	_, _ = r.Register("tally-1", "cam1", "1.0.0", now)
	_, _ = r.Register("tally-2", "cam3", "", now)
	_, _ = r.Assign("tally-1", "cam2")
	_ = r.Delete("tally-2")

	r2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := r2.List()
	if len(list) != 1 || list[0].DeviceID != "tally-1" || list[0].SourceID != "cam2" ||
		list[0].Firmware != "1.0.0" || !list[0].LastSeen.Equal(now) {
		t.Fatalf("reloaded = %+v", list)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestUnknownDevice(t *testing.T) {
	r, _ := Open(t.TempDir())
	if _, err := r.Assign("nope", "cam1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Assign: %v", err)
	}
	if err := r.Delete("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete: %v", err)
	}
	if err := r.Seen("nope", time.Now()); err != nil {
		t.Errorf("Seen: %v", err)
	}
	if len(r.List()) != 0 {
		t.Error("Seen must not create a device")
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("a corrupt registry must not be silently replaced by an empty one")
	}
}
