package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchDecodesStateAndSendsToken(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/switch/state" {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"programSource":"cam1","tallyState":{"cam1":"program","cam2":"preview"},
			"sources":{"cam1":{"key":"cam1","label":"Camera 1"},"cam2":{"key":"cam2"}},"audioChannels":{}}`))
	}))
	defer srv.Close()

	p := &Poller{URL: srv.URL + "/", Token: "secret", Client: srv.Client()}
	snap, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q", auth)
	}
	if snap.Tally["cam1"] != "program" || snap.Tally["cam2"] != "preview" {
		t.Errorf("tally = %v", snap.Tally)
	}
	if snap.Labels["cam1"] != "Camera 1" || snap.Labels["cam2"] != "" {
		t.Errorf("labels = %v", snap.Labels)
	}
}

func TestFetchRejectsIncompleteState(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"no tally":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"sources":{}}`)) },
		"not json":    func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
		"wrong shape": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"tallyState":[1]}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			p := &Poller{URL: srv.URL, Client: srv.Client()}
			if _, err := p.Fetch(context.Background()); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestRunEmitsOnlySuccessfulPolls(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"tallyState":{}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var emitted, observed, failed int
	p := &Poller{URL: srv.URL, Interval: time.Millisecond, Client: srv.Client(),
		Observe: func(_ time.Duration, err error) {
			observed++
			if err != nil {
				failed++
			}
			if observed == 6 {
				cancel()
			}
		}}
	p.Run(ctx, func(Snapshot) { emitted++ })
	if failed != 3 || emitted != 3 {
		t.Fatalf("observed=%d failed=%d emitted=%d", observed, failed, emitted)
	}
}
