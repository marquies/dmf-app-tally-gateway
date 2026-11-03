package config

import (
	"maps"
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

func TestOIDC(t *testing.T) {
	base := map[string]string{
		"SWITCHFRAME_URL":   "http://sf:8080",
		"OIDC_ISSUER":       "https://kc.example/realms/dmf",
		"OIDC_CLIENT_ID":    "switchframe.ns.sf",
		"OIDC_REDIRECT_URL": "https://tally.example/auth/callback",
	}
	c, err := Load(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDCIssuer != base["OIDC_ISSUER"] || c.OIDCClientID != base["OIDC_CLIENT_ID"] || c.OIDCRedirectURL != base["OIDC_REDIRECT_URL"] {
		t.Fatalf("OIDC not read: %+v", c)
	}

	// A partial setting would leave the page open or unreachable without
	// saying why, so it fails at start.
	for _, missing := range []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_REDIRECT_URL"} {
		m := maps.Clone(base)
		delete(m, missing)
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "must be set together") {
			t.Errorf("without %s: err = %v", missing, err)
		}
	}
	// The callback is served at one path; another would never be answered.
	for _, bad := range []string{"/auth/callback", "https://tally.example/callback"} {
		m := maps.Clone(base)
		m["OIDC_REDIRECT_URL"] = bad
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "OIDC_REDIRECT_URL") {
			t.Errorf("redirect %q: err = %v", bad, err)
		}
	}
}
