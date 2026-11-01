package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/marquies/dmf-app-tally-gateway/internal/registry"
	"github.com/marquies/dmf-app-tally-gateway/internal/tally"
)

const (
	// sendBuffer bounds the messages queued for one device. A device that
	// falls this far behind is dropped so it cannot hold up the others.
	sendBuffer   = 32
	writeTimeout = 5 * time.Second
	readLimit    = 4096
)

// Hub serves tally devices over WebSocket. Every message carrying a sequence
// number is numbered and queued under mu, so each device receives sequence
// numbers in increasing order.
type Hub struct {
	session         string
	heartbeat       time.Duration
	timeout         time.Duration
	registerTimeout time.Duration
	store           *tally.Store
	registry        *registry.Registry
	metrics         *metrics
	log             *slog.Logger

	mu      sync.Mutex
	seq     uint64
	clients map[string]*client
}

type client struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
	// lastActive is the time of the last message or pong, in Unix nanoseconds.
	lastActive atomic.Int64
	registered atomic.Bool

	// Guarded by Hub.mu.
	deviceID string
	sourceID string
	last     tally.State
}

func (c *client) touch() { c.lastActive.Store(time.Now().UnixNano()) }

// closeWith closes the connection without blocking the caller: Close waits
// for the peer's close frame, which the read loop receives.
func (c *client) closeWith(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() { go c.conn.Close(code, reason) })
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	c := &client{conn: conn, send: make(chan []byte, sendBuffer)}
	c.touch()
	deadline := time.AfterFunc(h.registerTimeout, func() {
		if !c.registered.Load() {
			c.closeWith(websocket.StatusPolicyViolation, "no register received")
		}
	})
	defer deadline.Stop()
	go h.writeLoop(ctx, c)
	go h.pingLoop(ctx, c)

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		c.touch()
		h.handle(c, data)
	}
	h.detach(c)
	c.closeWith(websocket.StatusNormalClosure, "")
}

func (h *Hub) writeLoop(ctx context.Context, c *client) {
	check := time.NewTicker(max(h.timeout/10, 10*time.Millisecond))
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.closeWith(websocket.StatusInternalError, "write failed")
				return
			}
		case <-check.C:
			if time.Since(time.Unix(0, c.lastActive.Load())) > h.timeout {
				c.closeWith(websocket.StatusPolicyViolation, "client timeout")
				return
			}
		}
	}
}

func (h *Hub) pingLoop(ctx context.Context, c *client) {
	t := time.NewTicker(h.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, h.heartbeat)
			if c.conn.Ping(pctx) == nil {
				c.touch()
			}
			cancel()
		}
	}
}

func (h *Hub) handle(c *client, data []byte) {
	var m clientMessage
	if err := json.Unmarshal(data, &m); err != nil {
		h.sendError(c, codeInvalidMessage, "message is not a JSON object")
		return
	}
	if m.Version != protocolVersion {
		h.sendError(c, codeUnsupportedVersion, fmt.Sprintf("version %d is not supported, use %d", m.Version, protocolVersion))
		return
	}
	if m.Type != "register" {
		h.sendError(c, codeUnknownType, fmt.Sprintf("unknown type %q", m.Type))
		return
	}
	switch {
	case m.DeviceID == "":
		h.sendError(c, codeInvalidRegister, "deviceId is required")
		return
	case !deviceIDPattern.MatchString(m.DeviceID):
		h.sendError(c, codeInvalidRegister, "deviceId must match [A-Za-z0-9._-]{1,64}")
		return
	case m.SourceID == "":
		h.sendError(c, codeInvalidRegister, "sourceId is required")
		return
	}
	h.mu.Lock()
	other := c.registered.Load() && c.deviceID != m.DeviceID
	h.mu.Unlock()
	if other {
		h.sendError(c, codeInvalidRegister, "connection is already registered as another device")
		return
	}

	d, err := h.registry.Register(m.DeviceID, m.SourceID, m.Firmware, time.Now())
	if err != nil {
		h.log.Error("persist device registry", "error", err)
	}
	h.attach(c, d.DeviceID, d.SourceID)
	h.log.Info("device registered", "deviceId", d.DeviceID, "sourceId", d.SourceID, "firmware", m.Firmware)
}

// attach makes c the connection of deviceID, replacing an older one, and
// sends it registered plus its current tally.
func (h *Hub) attach(c *client, deviceID, sourceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.clients[deviceID]; ok && old != c {
		delete(h.clients, deviceID)
		old.closeWith(websocket.StatusNormalClosure, "replaced by a new connection")
	}
	c.deviceID = deviceID
	c.registered.Store(true)
	h.clients[deviceID] = c
	h.metrics.connected.Set(float64(len(h.clients)))
	h.assignLocked(c, sourceID)
}

func (h *Hub) assignLocked(c *client, sourceID string) {
	c.sourceID = sourceID
	h.enqueueLocked(c, registeredMessage{
		Version:             protocolVersion,
		Type:                "registered",
		DeviceID:            c.deviceID,
		SourceID:            sourceID,
		Session:             h.session,
		HeartbeatIntervalMs: h.heartbeat.Milliseconds(),
		TimeoutMs:           h.timeout.Milliseconds(),
	})
	h.sendTallyLocked(c, h.store.Get(sourceID))
}

func (h *Hub) sendTallyLocked(c *client, st tally.State) {
	h.seq++
	c.last = st
	if h.enqueueLocked(c, tallyMessage{
		Version:   protocolVersion,
		Type:      "tally",
		SourceID:  c.sourceID,
		Program:   st.Program,
		Preview:   st.Preview,
		State:     st.State,
		Valid:     st.Valid,
		Session:   h.session,
		Sequence:  h.seq,
		Timestamp: timestamp(),
	}) {
		h.metrics.tallySent.Inc()
	}
}

// enqueueLocked queues v for c, dropping c when its queue is full.
func (h *Hub) enqueueLocked(c *client, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // every message type is a plain struct
	}
	select {
	case c.send <- b:
		return true
	default:
		h.log.Warn("dropping slow device", "deviceId", c.deviceID)
		if h.clients[c.deviceID] == c {
			delete(h.clients, c.deviceID)
			h.metrics.connected.Set(float64(len(h.clients)))
		}
		c.closeWith(websocket.StatusPolicyViolation, "too slow")
		return false
	}
}

func (h *Hub) sendError(c *client, code, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enqueueLocked(c, errorMessage{Version: protocolVersion, Type: "error", Code: code, Message: msg})
}

func (h *Hub) detach(c *client) {
	if !c.registered.Load() {
		return
	}
	h.mu.Lock()
	id := c.deviceID
	if h.clients[id] == c {
		delete(h.clients, id)
		h.metrics.connected.Set(float64(len(h.clients)))
	}
	h.mu.Unlock()
	if err := h.registry.Seen(id, time.Now()); err != nil {
		h.log.Error("persist device registry", "error", err)
	}
	h.log.Info("device disconnected", "deviceId", id)
}

// Broadcast sends every device whose tally differs from what it last
// received its current tally.
func (h *Hub) Broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.clients {
		if st := h.store.Get(c.sourceID); st != c.last {
			h.sendTallyLocked(c, st)
		}
	}
}

// Reassign points a connected device at a new source and tells it so.
func (h *Hub) Reassign(deviceID, sourceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[deviceID]; ok {
		h.assignLocked(c, sourceID)
	}
}

// Disconnect closes the connection of a device, if any.
func (h *Hub) Disconnect(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[deviceID]; ok {
		delete(h.clients, deviceID)
		h.metrics.connected.Set(float64(len(h.clients)))
		c.closeWith(websocket.StatusNormalClosure, "device deleted")
	}
}

// Connected reports whether a device is connected and when it was last heard.
func (h *Hub) Connected(deviceID string) (bool, time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[deviceID]
	if !ok {
		return false, time.Time{}
	}
	return true, time.Unix(0, c.lastActive.Load()).UTC()
}

// RunHeartbeats sends a heartbeat to every registered device each interval.
func (h *Hub) RunHeartbeats(ctx context.Context) {
	t := time.NewTicker(h.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.mu.Lock()
			for _, c := range h.clients {
				h.seq++
				h.enqueueLocked(c, heartbeatMessage{
					Version:   protocolVersion,
					Type:      "heartbeat",
					Session:   h.session,
					Sequence:  h.seq,
					Timestamp: timestamp(),
				})
			}
			h.mu.Unlock()
		}
	}
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339) }
