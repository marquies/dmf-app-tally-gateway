package tally

import (
	"testing"

	"github.com/marquies/dmf-app-tally-gateway/internal/upstream"
)

func TestFor(t *testing.T) {
	for _, tc := range []struct {
		state            string
		valid            bool
		program, preview bool
		want             string
	}{
		{"program", true, true, false, "program"},
		{"key_fill", true, true, false, "key_fill"},
		{"preview", true, false, true, "preview"},
		{"key_alpha", true, false, false, "key_alpha"},
		{"idle", true, false, false, "idle"},
		{Unknown, true, false, false, Unknown},
		// Stale upstream state must never light a tally, whatever it last was.
		{"program", false, false, false, Unknown},
		{"preview", false, false, false, Unknown},
	} {
		got := For(tc.state, tc.valid)
		if got.Program != tc.program || got.Preview != tc.preview || got.Valid != tc.valid || got.State != tc.want {
			t.Errorf("For(%q, %v) = %+v", tc.state, tc.valid, got)
		}
	}
}

func snap(tally map[string]string) upstream.Snapshot {
	return upstream.Snapshot{Tally: tally, Labels: map[string]string{"cam1": "Camera 1"}}
}

func TestApplyReportsOnlyTallyChanges(t *testing.T) {
	s := NewStore()
	if s.Valid() || s.Get("cam1").Valid {
		t.Fatal("a new store has no fresh state")
	}
	if !s.Apply(snap(map[string]string{"cam1": "program", "cam2": "preview"})) {
		t.Fatal("first snapshot is a change")
	}
	if s.Apply(snap(map[string]string{"cam1": "program", "cam2": "preview"})) {
		t.Fatal("identical snapshot is not a change")
	}
	labelsOnly := upstream.Snapshot{Tally: map[string]string{"cam1": "program", "cam2": "preview"}, Labels: map[string]string{"cam1": "Renamed"}}
	if s.Apply(labelsOnly) {
		t.Fatal("a label change is not a tally change")
	}
	if !s.Apply(snap(map[string]string{"cam1": "preview", "cam2": "program"})) {
		t.Fatal("swapped program and preview is a change")
	}
	if !s.Apply(snap(map[string]string{"cam1": "preview"})) {
		t.Fatal("a removed source is a change")
	}
	if got := s.Get("cam2"); got.State != Unknown || !got.Valid {
		t.Fatalf("removed source = %+v", got)
	}
}

func TestInvalidateAndRecover(t *testing.T) {
	s := NewStore()
	s.Apply(snap(map[string]string{"cam1": "program"}))
	if !s.Invalidate() {
		t.Fatal("invalidating fresh state is a change")
	}
	if s.Invalidate() {
		t.Fatal("invalidating stale state is not a change")
	}
	if got := s.Get("cam1"); got.Valid || got.Program {
		t.Fatalf("stale tally = %+v", got)
	}
	// Recovery with the same tally as before the outage is still a change:
	// devices are showing fail-safe and must be told.
	if !s.Apply(snap(map[string]string{"cam1": "program"})) {
		t.Fatal("recovery is a change")
	}
	if got := s.Get("cam1"); !got.Valid || !got.Program {
		t.Fatalf("recovered tally = %+v", got)
	}
}

func TestSources(t *testing.T) {
	s := NewStore()
	s.Apply(snap(map[string]string{"cam2": "preview", "cam1": "key_fill"}))
	got := s.Sources()
	if len(got) != 2 || got[0].SourceID != "cam1" || got[0].Label != "Camera 1" || !got[0].Program ||
		got[1].SourceID != "cam2" || !got[1].Preview {
		t.Fatalf("sources = %+v", got)
	}
	s.Invalidate()
	if got := s.Sources(); len(got) != 2 || got[0].Valid {
		t.Fatalf("stale sources = %+v", got)
	}
}
