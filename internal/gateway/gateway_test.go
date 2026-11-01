package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/marquies/dmf-app-tally-gateway/internal/config"
	"github.com/marquies/dmf-app-tally-gateway/internal/registry"
	"github.com/marquies/dmf-app-tally-gateway/internal/upstream"
)

// fakeSwitchFrame serves GET /api/switch/state from a mutable tally map.
type fakeSwitchFrame struct {
	mu    sync.Mutex
	tally map[string]string
	down  bool
	srv   *httptest.Server
}

func newFakeSwitchFrame(t *testing.T, tally map[string]string) *fakeSwitchFrame {
	f := &fakeSwitchFrame{tally: tally}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		sources := map[string]map[string]string{}
		for k := range f.tally {
			sources[k] = map[string]string{"key": k, "label": strings.ToUpper(k)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tallyState": f.tally, "sources": sources})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSwitchFrame) set(source, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tally = maps.Clone(f.tally)
	f.tally[source] = state
}

func (f *fakeSwitchFrame) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

type testEnv struct {
	sf  *fakeSwitchFrame
	g   *Gateway
	srv *httptest.Server
	cfg config.Config
}

func testConfig(dir string) config.Config {
	return config.Config{
		PollInterval:         10 * time.Millisecond,
		UpstreamStaleTimeout: 300 * time.Millisecond,
		HeartbeatInterval:    time.Second,
		ClientTimeout:        5 * time.Second,
		DataDir:              dir,
	}
}

func startGateway(t *testing.T, sf *fakeSwitchFrame, cfg config.Config) *testEnv {
	t.Helper()
	cfg.SwitchFrameURL = sf.srv.URL
	reg, err := registry.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	g := New(cfg, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	poller := &upstream.Poller{URL: cfg.SwitchFrameURL, Interval: cfg.PollInterval,
		Client: &http.Client{Timeout: cfg.UpstreamStaleTimeout}, Observe: g.ObservePoll}
	go g.Run(ctx, poller)
	srv := httptest.NewServer(g.Handler())
	t.Cleanup(func() {
		cancel()
		srv.CloseClientConnections()
		srv.Close()
	})
	e := &testEnv{sf: sf, g: g, srv: srv, cfg: cfg}
	e.waitReady(t)
	return e
}

func newEnv(t *testing.T, mod func(*config.Config)) *testEnv {
	sf := newFakeSwitchFrame(t, map[string]string{"cam1": "program", "cam2": "preview", "cam3": "idle"})
	cfg := testConfig(t.TempDir())
	if mod != nil {
		mod(&cfg)
	}
	return startGateway(t, sf, cfg)
}

func (e *testEnv) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e.g.store.Valid() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gateway never received upstream state")
}

// msg is the union of every gateway -> device message.
type msg struct {
	Version             int    `json:"version"`
	Type                string `json:"type"`
	DeviceID            string `json:"deviceId"`
	SourceID            string `json:"sourceId"`
	Session             string `json:"session"`
	HeartbeatIntervalMs int64  `json:"heartbeatIntervalMs"`
	TimeoutMs           int64  `json:"timeoutMs"`
	Program             bool   `json:"program"`
	Preview             bool   `json:"preview"`
	State               string `json:"state"`
	Valid               bool   `json:"valid"`
	Sequence            uint64 `json:"sequence"`
	Timestamp           string `json:"timestamp"`
	Code                string `json:"code"`
	Message             string `json:"message"`
}

// device reads in the background: a Read whose context expires closes a
// coder/websocket connection, so waiting for nothing must not use one.
type device struct {
	t    *testing.T
	conn *websocket.Conn
	in   chan []byte
	seq  uint64
}

func newDevice(t *testing.T, conn *websocket.Conn) *device {
	d := &device{t: t, conn: conn, in: make(chan []byte, 256)}
	go func() {
		defer close(d.in)
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			d.in <- b
		}
	}()
	return d
}

func (e *testEnv) dial(t *testing.T, header http.Header, query string) (*device, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws/v1" + query
	conn, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	t.Cleanup(func() { conn.CloseNow() })
	return newDevice(t, conn), resp, nil
}

func (e *testEnv) connect(t *testing.T, deviceID, sourceID string) *device {
	t.Helper()
	d, _, err := e.dial(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d.send(map[string]any{"version": 1, "type": "register", "deviceId": deviceID, "sourceId": sourceID, "firmware": "1.0.0"})
	return d
}

func (d *device) send(v any) {
	d.t.Helper()
	b, _ := json.Marshal(v)
	if err := d.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		d.t.Fatal(err)
	}
}

// read returns the next message other than a heartbeat and checks that
// sequence numbers only increase.
func (d *device) read(timeout time.Duration) (msg, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		var b []byte
		select {
		case <-timer.C:
			return msg{}, errTimeout
		case got, ok := <-d.in:
			if !ok {
				return msg{}, errClosed
			}
			b = got
		}
		var m msg
		if err := json.Unmarshal(b, &m); err != nil {
			d.t.Fatalf("gateway sent invalid JSON %q", b)
		}
		if m.Version != 1 {
			d.t.Fatalf("message without version 1: %s", b)
		}
		if m.Sequence != 0 {
			if m.Sequence <= d.seq {
				d.t.Fatalf("sequence %d after %d", m.Sequence, d.seq)
			}
			d.seq = m.Sequence
		}
		if m.Type != "heartbeat" {
			return m, nil
		}
	}
}

func (d *device) expect(typ string) msg {
	d.t.Helper()
	m, err := d.read(2 * time.Second)
	if err != nil {
		d.t.Fatalf("waiting for %s: %v", typ, err)
	}
	if m.Type != typ {
		d.t.Fatalf("got %+v, want type %s", m, typ)
	}
	return m
}

func (d *device) expectNothing(within time.Duration) {
	d.t.Helper()
	if m, err := d.read(within); err == nil {
		d.t.Fatalf("unexpected message %+v", m)
	}
}

var (
	errTimeout = errors.New("timeout")
	errClosed  = errors.New("closed")
)

func (d *device) expectClosed() {
	d.t.Helper()
	for {
		if _, err := d.read(3 * time.Second); err != nil {
			if err == errTimeout {
				d.t.Fatal("connection was not closed")
			}
			return
		}
	}
}

func (e *testEnv) request(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if e.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.AuthToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRegisterReceivesStateAndChanges(t *testing.T) {
	e := newEnv(t, nil)
	d := e.connect(t, "tally-1", "cam1")

	reg := d.expect("registered")
	if reg.DeviceID != "tally-1" || reg.SourceID != "cam1" || reg.Session != e.g.hub.session ||
		reg.HeartbeatIntervalMs != 1000 || reg.TimeoutMs != 5000 {
		t.Fatalf("registered = %+v", reg)
	}
	tl := d.expect("tally")
	if tl.SourceID != "cam1" || !tl.Program || tl.Preview || tl.State != "program" || !tl.Valid ||
		tl.Session != reg.Session || tl.Sequence == 0 {
		t.Fatalf("initial tally = %+v", tl)
	}
	if _, err := time.Parse(time.RFC3339, tl.Timestamp); err != nil {
		t.Fatalf("timestamp: %v", err)
	}

	e.sf.set("cam1", "preview")
	if tl := d.expect("tally"); tl.Program || !tl.Preview || tl.State != "preview" {
		t.Fatalf("after cut = %+v", tl)
	}

	// Another source changing is not a change for this device.
	e.sf.set("cam2", "program")
	d.expectNothing(200 * time.Millisecond)

	// A key fill is on air.
	e.sf.set("cam1", "key_fill")
	if tl := d.expect("tally"); !tl.Program || tl.State != "key_fill" {
		t.Fatalf("key fill = %+v", tl)
	}

	// A source SwitchFrame does not list is unknown, yet the state is fresh.
	u := e.connect(t, "tally-2", "nope")
	u.expect("registered")
	if tl := u.expect("tally"); tl.State != "unknown" || !tl.Valid || tl.Program || tl.Preview {
		t.Fatalf("unknown source = %+v", tl)
	}
}

func TestUpstreamDownInvalidatesUntilFreshSnapshot(t *testing.T) {
	e := newEnv(t, nil)
	d := e.connect(t, "tally-1", "cam1")
	d.expect("registered")
	d.expect("tally")

	e.sf.setDown(true)
	tl := d.expect("tally")
	if tl.Valid || tl.Program || tl.Preview {
		t.Fatalf("stale tally = %+v", tl)
	}
	if resp := e.request(t, "GET", "/readyz", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz while stale = %d", resp.StatusCode)
	}

	// A device registering during the outage gets fail-safe straight away.
	late := e.connect(t, "tally-2", "cam2")
	late.expect("registered")
	if tl := late.expect("tally"); tl.Valid {
		t.Fatalf("tally during outage = %+v", tl)
	}

	e.sf.setDown(false)
	tl = d.expect("tally")
	if !tl.Valid || !tl.Program {
		t.Fatalf("recovered tally = %+v", tl)
	}
	if resp := e.request(t, "GET", "/readyz", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz after recovery = %d", resp.StatusCode)
	}
}

func TestReassignmentIsPushedAndPersisted(t *testing.T) {
	e := newEnv(t, nil)
	d := e.connect(t, "tally-1", "cam1")
	d.expect("registered")
	d.expect("tally")

	resp := e.request(t, "PUT", "/api/v1/devices/tally-1", `{"sourceId":"cam2"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", resp.StatusCode)
	}
	if reg := d.expect("registered"); reg.SourceID != "cam2" {
		t.Fatalf("registered after reassignment = %+v", reg)
	}
	if tl := d.expect("tally"); tl.SourceID != "cam2" || !tl.Preview {
		t.Fatalf("tally after reassignment = %+v", tl)
	}
	// The device now follows cam2 only.
	e.sf.set("cam1", "idle")
	d.expectNothing(200 * time.Millisecond)

	var devices []deviceView
	_ = json.NewDecoder(e.request(t, "GET", "/api/v1/devices", "").Body).Decode(&devices)
	if len(devices) != 1 || devices[0].SourceID != "cam2" || !devices[0].Connected ||
		devices[0].Firmware != "1.0.0" || !devices[0].Tally.Preview || devices[0].LastSeen.IsZero() {
		t.Fatalf("devices = %+v", devices)
	}

	for path, want := range map[string]int{
		"/api/v1/devices/nope":    http.StatusNotFound,
		"/api/v1/devices/tally-1": http.StatusBadRequest,
	} {
		body := `{"sourceId":"cam1"}`
		if want == http.StatusBadRequest {
			body = `{}`
		}
		if resp := e.request(t, "PUT", path, body); resp.StatusCode != want {
			t.Errorf("PUT %s %s = %d, want %d", path, body, resp.StatusCode, want)
		}
	}

	// After a restart the device still registers with its own cam1, and the
	// persisted assignment wins.
	e2 := startGateway(t, e.sf, e.cfg)
	d2 := e2.connect(t, "tally-1", "cam1")
	reg := d2.expect("registered")
	if reg.SourceID != "cam2" || reg.Session == e.g.hub.session {
		t.Fatalf("registered after restart = %+v", reg)
	}

	if resp := e2.request(t, "DELETE", "/api/v1/devices/tally-1", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	d2.expect("tally")
	d2.expectClosed()
	if resp := e2.request(t, "DELETE", "/api/v1/devices/tally-1", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE = %d", resp.StatusCode)
	}
	// Forgotten, the device starts over with its own source.
	d3 := e2.connect(t, "tally-1", "cam1")
	if reg := d3.expect("registered"); reg.SourceID != "cam1" {
		t.Fatalf("registered after delete = %+v", reg)
	}
}

func TestSources(t *testing.T) {
	e := newEnv(t, nil)
	var body struct {
		Valid   bool `json:"valid"`
		Sources []struct {
			SourceID string `json:"sourceId"`
			Label    string `json:"label"`
			State    string `json:"state"`
			Program  bool   `json:"program"`
			Valid    bool   `json:"valid"`
		} `json:"sources"`
	}
	_ = json.NewDecoder(e.request(t, "GET", "/api/v1/sources", "").Body).Decode(&body)
	if !body.Valid || len(body.Sources) != 3 || body.Sources[0].SourceID != "cam1" ||
		body.Sources[0].Label != "CAM1" || !body.Sources[0].Program || !body.Sources[0].Valid {
		t.Fatalf("sources = %+v", body)
	}
}

func TestDuplicateDeviceReplacesOlderConnection(t *testing.T) {
	e := newEnv(t, nil)
	first := e.connect(t, "tally-1", "cam1")
	first.expect("registered")
	first.expect("tally")

	second := e.connect(t, "tally-1", "cam1")
	second.expect("registered")
	second.expect("tally")
	first.expectClosed()

	e.sf.set("cam1", "preview")
	if tl := second.expect("tally"); !tl.Preview {
		t.Fatalf("tally on the new connection = %+v", tl)
	}
}

func TestProtocolErrorsKeepConnectionOpen(t *testing.T) {
	e := newEnv(t, nil)
	d, _, err := e.dial(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		send string
		code string
	}{
		{`not json`, "invalid_message"},
		{`{"version":2,"type":"register","deviceId":"a","sourceId":"b"}`, "unsupported_version"},
		{`{"version":1,"type":"subscribe"}`, "unknown_type"},
		{`{"version":1,"type":"register","sourceId":"cam1"}`, "invalid_register"},
		{`{"version":1,"type":"register","deviceId":"bad id","sourceId":"cam1"}`, "invalid_register"},
		{`{"version":1,"type":"register","deviceId":"` + strings.Repeat("a", 65) + `","sourceId":"cam1"}`, "invalid_register"},
		{`{"version":1,"type":"register","deviceId":"tally-1"}`, "invalid_register"},
	} {
		if err := d.conn.Write(context.Background(), websocket.MessageText, []byte(tc.send)); err != nil {
			t.Fatal(err)
		}
		if m := d.expect("error"); m.Code != tc.code || m.Message == "" {
			t.Errorf("%s -> %+v, want %s", tc.send, m, tc.code)
		}
	}
	d.send(map[string]any{"version": 1, "type": "register", "deviceId": "tally-1", "sourceId": "cam1", "extra": true})
	d.expect("registered")
	d.expect("tally")
}

func TestRegisterTimeoutClosesConnection(t *testing.T) {
	e := newEnv(t, nil)
	e.g.hub.registerTimeout = 100 * time.Millisecond
	d, _, err := e.dial(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d.expectClosed()
}

func TestSilentClientIsClosedAndHeartbeatsFlow(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.HeartbeatInterval = 50 * time.Millisecond
		c.ClientTimeout = 300 * time.Millisecond
	})
	// A device that reads answers pings and stays connected well past the
	// timeout, receiving heartbeats.
	alive := e.connect(t, "alive", "cam1")
	time.Sleep(time.Second)
	heartbeats := 0
	for len(alive.in) > 0 {
		if strings.Contains(string(<-alive.in), `"heartbeat"`) {
			heartbeats++
		}
	}
	if heartbeats < 5 {
		t.Fatalf("got %d heartbeats in 1s at 50ms", heartbeats)
	}
	if ok, _ := e.g.hub.Connected("alive"); !ok {
		t.Fatal("a responsive device was disconnected")
	}

	// A device that stops reading answers no pings and is closed.
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = conn.Write(context.Background(), websocket.MessageText,
		[]byte(`{"version":1,"type":"register","deviceId":"silent","sourceId":"cam1"}`))
	time.Sleep(50 * time.Millisecond)
	if ok, _ := e.g.hub.Connected("silent"); !ok {
		t.Fatal("silent device never registered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := e.g.hub.Connected("silent"); !ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("silent device still connected after the client timeout")
}

func TestAuth(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AuthToken = "s3cret" })

	if _, resp, err := e.dial(t, nil, ""); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: err=%v resp=%v", err, resp)
	}
	if _, resp, err := e.dial(t, http.Header{"Authorization": {"Bearer wrong"}}, ""); err == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: err=%v", err)
	}
	d, _, err := e.dial(t, http.Header{"Authorization": {"Bearer s3cret"}}, "")
	if err != nil {
		t.Fatalf("bearer token: %v", err)
	}
	d.conn.CloseNow()
	if _, _, err := e.dial(t, nil, "?token=s3cret"); err != nil {
		t.Fatalf("query token: %v", err)
	}

	for _, path := range []string{"/", "/api/v1/devices", "/api/v1/sources"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without token = %d", path, resp.StatusCode)
		}
		if resp := e.request(t, "GET", path, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with token = %d", path, resp.StatusCode)
		}
	}
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d; probes and scrapes carry no token", path, resp.StatusCode)
		}
	}
}

func TestMetrics(t *testing.T) {
	e := newEnv(t, nil)
	d := e.connect(t, "tally-1", "cam1")
	d.expect("registered")
	d.expect("tally")
	b, _ := io.ReadAll(e.request(t, "GET", "/metrics", "").Body)
	for _, want := range []string{
		"tally_gateway_connected_devices 1",
		"tally_gateway_tally_messages_sent_total 1",
		"tally_gateway_upstream_valid 1",
		"tally_gateway_upstream_poll_errors_total 0",
		"tally_gateway_upstream_poll_duration_seconds_count",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestManyDevices(t *testing.T) {
	e := newEnv(t, nil)
	const n = 40
	devices := make([]*device, n)
	for i := range devices {
		devices[i] = e.connect(t, fmt.Sprintf("tally-%02d", i), "cam1")
		devices[i].expect("registered")
		devices[i].expect("tally")
	}
	e.sf.set("cam1", "preview")
	for _, d := range devices {
		if tl := d.expect("tally"); !tl.Preview {
			t.Fatalf("tally = %+v", tl)
		}
	}
}

// TestChangeToDeviceLatency measures from a change in SwitchFrame to the
// tally arriving at a device, at the default poll interval. The budget for
// that path is 100 ms at p95.
func TestChangeToDeviceLatency(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.PollInterval = 50 * time.Millisecond })
	d := e.connect(t, "tally-1", "cam1")
	d.expect("registered")
	d.expect("tally")

	var samples []time.Duration
	states := []string{"preview", "program"}
	for i := range 40 {
		start := time.Now()
		e.sf.set("cam1", states[i%2])
		d.expect("tally")
		samples = append(samples, time.Since(start))
	}
	slices.Sort(samples)
	p95 := samples[len(samples)*95/100-1]
	t.Logf("change-to-device latency: p50 %v, p95 %v, max %v", samples[len(samples)/2], p95, samples[len(samples)-1])
	if p95 > 100*time.Millisecond {
		t.Fatalf("p95 %v exceeds 100ms", p95)
	}
}
