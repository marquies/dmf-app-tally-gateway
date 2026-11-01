// Package upstream reads tally state from SwitchFrame.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Snapshot is the complete tally state of every SwitchFrame source at one
// point in time.
type Snapshot struct {
	// Tally maps a source key to its SwitchFrame tallyState value.
	Tally map[string]string
	// Labels maps a source key to its display label.
	Labels map[string]string
}

// Source delivers snapshots until ctx ends. Polling is the only
// implementation because SwitchFrame offers no push interface reachable
// without changes to it; the gateway depends on this interface alone so a
// push source can replace the poller.
type Source interface {
	Run(ctx context.Context, emit func(Snapshot))
}

// Observer receives the outcome of every poll, for metrics.
type Observer func(d time.Duration, err error)

// Poller fetches GET /api/switch/state at a fixed interval.
type Poller struct {
	URL      string
	Token    string
	Interval time.Duration
	Client   *http.Client
	Observe  Observer
}

// state decodes only the fields the gateway uses out of SwitchFrame's
// ControlRoomState.
type state struct {
	TallyState map[string]string `json:"tallyState"`
	Sources    map[string]struct {
		Label string `json:"label"`
	} `json:"sources"`
}

func (p *Poller) Run(ctx context.Context, emit func(Snapshot)) {
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		start := time.Now()
		snap, err := p.Fetch(ctx)
		if ctx.Err() != nil {
			return
		}
		if p.Observe != nil {
			p.Observe(time.Since(start), err)
		}
		if err == nil {
			emit(snap)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Fetch performs one poll.
func (p *Poller) Fetch(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/switch/state", nil)
	if err != nil {
		return Snapshot{}, err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Snapshot{}, fmt.Errorf("switchframe: %s", resp.Status)
	}
	var s state
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("switchframe: decode state: %w", err)
	}
	// SwitchFrame always serialises tallyState, so its absence means the
	// response is not a ControlRoomState and carries no usable tally.
	if s.TallyState == nil {
		return Snapshot{}, errors.New("switchframe: state has no tallyState")
	}
	snap := Snapshot{Tally: s.TallyState, Labels: make(map[string]string, len(s.Sources))}
	for k, src := range s.Sources {
		snap.Labels[k] = src.Label
	}
	return snap, nil
}
