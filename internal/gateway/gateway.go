// Package gateway distributes SwitchFrame tally state to tally devices and
// serves the management API, the monitoring UI and the operational endpoints.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/marquies/dmf-app-tally-gateway/internal/config"
	"github.com/marquies/dmf-app-tally-gateway/internal/registry"
	"github.com/marquies/dmf-app-tally-gateway/internal/tally"
	"github.com/marquies/dmf-app-tally-gateway/internal/upstream"
)

//go:embed ui/index.html
var indexHTML []byte

type Gateway struct {
	cfg      config.Config
	store    *tally.Store
	registry *registry.Registry
	hub      *Hub
	metrics  *metrics
	log      *slog.Logger

	// upMu orders snapshot application against the staleness check, so a
	// snapshot that has just arrived is never invalidated as stale.
	upMu      sync.Mutex
	lastFresh time.Time
}

func New(cfg config.Config, reg *registry.Registry, log *slog.Logger) *Gateway {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	m := newMetrics()
	store := tally.NewStore()
	return &Gateway{
		cfg:      cfg,
		store:    store,
		registry: reg,
		metrics:  m,
		log:      log,
		hub: &Hub{
			session:         hex.EncodeToString(b),
			heartbeat:       cfg.HeartbeatInterval,
			timeout:         cfg.ClientTimeout,
			registerTimeout: 10 * time.Second,
			store:           store,
			registry:        reg,
			metrics:         m,
			log:             log,
			clients:         map[string]*client{},
		},
	}
}

// ObservePoll records the outcome of one upstream poll.
func (g *Gateway) ObservePoll(d time.Duration, err error) {
	g.metrics.pollDuration.Observe(d.Seconds())
	if err != nil {
		g.metrics.pollErrors.Inc()
		g.log.Debug("switchframe poll failed", "error", err)
	}
}

// Run consumes src and sends heartbeats until ctx ends.
func (g *Gateway) Run(ctx context.Context, src upstream.Source) {
	go g.hub.RunHeartbeats(ctx)
	go g.watchStaleness(ctx)
	src.Run(ctx, g.apply)
}

func (g *Gateway) apply(snap upstream.Snapshot) {
	g.upMu.Lock()
	g.lastFresh = time.Now()
	wasValid := g.store.Valid()
	changed := g.store.Apply(snap)
	g.upMu.Unlock()
	if !wasValid {
		g.metrics.upstreamValid.Set(1)
		g.log.Info("switchframe state fresh")
	}
	if changed {
		g.hub.Broadcast()
	}
}

func (g *Gateway) watchStaleness(ctx context.Context) {
	t := time.NewTicker(max(g.cfg.UpstreamStaleTimeout/10, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.upMu.Lock()
			stale := g.store.Valid() && time.Since(g.lastFresh) > g.cfg.UpstreamStaleTimeout && g.store.Invalidate()
			g.upMu.Unlock()
			if stale {
				g.metrics.upstreamValid.Set(0)
				g.log.Warn("switchframe state stale, tally invalid", "timeout", g.cfg.UpstreamStaleTimeout.String())
				g.hub.Broadcast()
			}
		}
	}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !g.store.Valid() {
			http.Error(w, "switchframe state not fresh", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(g.metrics.reg, promhttp.HandlerOpts{}))

	mux.Handle("GET /ws/v1", g.auth(g.hub))
	mux.Handle("GET /{$}", g.auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})))
	mux.Handle("GET /api/v1/devices", g.auth(http.HandlerFunc(g.listDevices)))
	mux.Handle("PUT /api/v1/devices/{id}", g.auth(http.HandlerFunc(g.assignDevice)))
	mux.Handle("DELETE /api/v1/devices/{id}", g.auth(http.HandlerFunc(g.deleteDevice)))
	mux.Handle("GET /api/v1/sources", g.auth(http.HandlerFunc(g.listSources)))
	return mux
}

// auth accepts the token as a bearer token or, for clients that cannot set
// headers, as the token query parameter.
func (g *Gateway) auth(next http.Handler) http.Handler {
	if g.cfg.AuthToken == "" {
		return next
	}
	want := []byte(g.cfg.AuthToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("token")
		if h, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = h
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type deviceView struct {
	registry.Device
	Connected bool        `json:"connected"`
	Tally     tally.State `json:"tally"`
}

func (g *Gateway) view(d registry.Device) deviceView {
	connected, seen := g.hub.Connected(d.DeviceID)
	if connected {
		d.LastSeen = seen
	}
	return deviceView{Device: d, Connected: connected, Tally: g.store.Get(d.SourceID)}
}

func (g *Gateway) listDevices(w http.ResponseWriter, _ *http.Request) {
	devices := g.registry.List()
	out := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		out = append(out, g.view(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (g *Gateway) assignDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceID string `json:"sourceId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.SourceID == "" {
		http.Error(w, `body must be {"sourceId":"<source key>"}`, http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	d, err := g.registry.Assign(id, body.SourceID)
	if errors.Is(err, registry.ErrNotFound) {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if err != nil {
		g.log.Error("persist device registry", "error", err)
		http.Error(w, "cannot persist assignment", http.StatusInternalServerError)
		return
	}
	g.hub.Reassign(id, body.SourceID)
	g.log.Info("device assigned", "deviceId", id, "sourceId", body.SourceID)
	writeJSON(w, http.StatusOK, g.view(d))
}

// deleteDevice forgets a device and closes its connection, so a device that
// reconnects registers afresh with its own configured source.
func (g *Gateway) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := g.registry.Delete(id)
	if errors.Is(err, registry.ErrNotFound) {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if err != nil {
		g.log.Error("persist device registry", "error", err)
		http.Error(w, "cannot persist deletion", http.StatusInternalServerError)
		return
	}
	g.hub.Disconnect(id)
	g.log.Info("device deleted", "deviceId", id)
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) listSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Valid   bool           `json:"valid"`
		Sources []tally.Source `json:"sources"`
	}{g.store.Valid(), g.store.Sources()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
