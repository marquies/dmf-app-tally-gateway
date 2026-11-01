package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"SWITCHFRAME_URL": "http://sf:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	// The poll interval default is what keeps the change-to-device latency
	// within its 100 ms budget.
	if c.PollInterval != 50*time.Millisecond || c.UpstreamStaleTimeout != 3*time.Second ||
		c.HeartbeatInterval != 10*time.Second || c.ClientTimeout != 30*time.Second ||
		c.ListenAddr != ":8080" || c.DataDir != "/data" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"SWITCHFRAME_URL": "http://sf:8080",
		"POLL_INTERVAL":   "20ms",
		"CLIENT_TIMEOUT":  "5s",
		"DATA_DIR":        "/tmp/x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.PollInterval != 20*time.Millisecond || c.ClientTimeout != 5*time.Second || c.DataDir != "/tmp/x" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestInvalid(t *testing.T) {
	_, err := Load(env(map[string]string{
		"POLL_INTERVAL": "fast",
		"TLS_CERT_FILE": "/cert.pem",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"SWITCHFRAME_URL", "POLL_INTERVAL", "TLS_KEY_FILE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
