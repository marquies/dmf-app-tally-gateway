package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// serverConn returns the gateway side of a WebSocket connection nobody
// writes to or reads from.
func serverConn(t *testing.T) *websocket.Conn {
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		conns <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.CloseNow() })
	return <-conns
}

func TestSlowDeviceIsDroppedWithoutDelayingOthers(t *testing.T) {
	e := newEnv(t, nil)
	fast := e.connect(t, "fast", "cam1")
	fast.expect("registered")
	fast.expect("tally")

	// A device whose queue is full: nothing drains it.
	slow := &client{conn: serverConn(t), send: make(chan []byte, sendBuffer), deviceID: "slow", sourceID: "cam1"}
	slow.registered.Store(true)
	slow.touch()
	for range sendBuffer {
		slow.send <- []byte("{}")
	}
	e.g.hub.mu.Lock()
	e.g.hub.clients["slow"] = slow
	e.g.hub.mu.Unlock()

	e.sf.set("cam1", "preview")
	m, err := fast.read(time.Second)
	if err != nil || m.Type != "tally" || !m.Preview {
		t.Fatalf("fast device got %+v, %v", m, err)
	}
	if ok, _ := e.g.hub.Connected("slow"); ok {
		t.Fatal("slow device was not dropped")
	}
}
