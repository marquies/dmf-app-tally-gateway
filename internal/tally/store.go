// Package tally holds the latest upstream tally state per source.
package tally

import (
	"maps"
	"sort"
	"sync"

	"github.com/marquies/dmf-app-tally-gateway/internal/upstream"
)

// Unknown is the state reported for a source SwitchFrame does not list.
const Unknown = "unknown"

// State is the tally of one source as a device sees it.
type State struct {
	State   string `json:"state"`
	Program bool   `json:"program"`
	Preview bool   `json:"preview"`
	Valid   bool   `json:"valid"`
}

// For maps a SwitchFrame tallyState value to a device tally. A key fill is on
// air, so it lights program; a key alpha is a matte and lights nothing.
// Without fresh upstream state no state is known and nothing lights.
func For(state string, valid bool) State {
	if !valid {
		return State{State: Unknown}
	}
	return State{
		State:   state,
		Program: state == "program" || state == "key_fill",
		Preview: state == "preview",
		Valid:   true,
	}
}

// Source is one SwitchFrame source with its current tally.
type Source struct {
	SourceID string `json:"sourceId"`
	Label    string `json:"label"`
	State
}

// Store holds the latest snapshot and whether it is fresh.
type Store struct {
	mu     sync.RWMutex
	tally  map[string]string
	labels map[string]string
	valid  bool
}

func NewStore() *Store {
	return &Store{tally: map[string]string{}, labels: map[string]string{}}
}

// Apply replaces the state with a complete fresh snapshot and reports whether
// the tally of any source changed. Label changes alone do not count.
func (s *Store) Apply(snap upstream.Snapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := !s.valid || !maps.Equal(s.tally, snap.Tally)
	s.tally = maps.Clone(snap.Tally)
	s.labels = maps.Clone(snap.Labels)
	s.valid = true
	return changed
}

// Invalidate marks the state stale and reports whether it was fresh before.
// The labels of the last snapshot are kept so the API still names the
// sources.
func (s *Store) Invalidate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.valid
	s.valid = false
	return was
}

func (s *Store) Valid() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.valid
}

// Get returns the device tally for a source.
func (s *Store) Get(sourceID string) State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(sourceID)
}

func (s *Store) get(sourceID string) State {
	st, ok := s.tally[sourceID]
	if !ok {
		st = Unknown
	}
	return For(st, s.valid)
}

// Sources lists every source of the last snapshot, sorted by key.
func (s *Store) Sources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make(map[string]struct{}, len(s.tally))
	for k := range s.tally {
		keys[k] = struct{}{}
	}
	for k := range s.labels {
		keys[k] = struct{}{}
	}
	out := make([]Source, 0, len(keys))
	for k := range keys {
		out = append(out, Source{SourceID: k, Label: s.labels[k], State: s.get(k)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out
}
