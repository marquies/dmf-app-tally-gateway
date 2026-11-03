// Package config reads the gateway configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

type Config struct {
	ListenAddr           string
	SwitchFrameURL       string
	SwitchFrameToken     string
	PollInterval         time.Duration
	UpstreamStaleTimeout time.Duration
	HeartbeatInterval    time.Duration
	ClientTimeout        time.Duration
	DataDir              string
	AuthToken            string
	TLSCertFile          string
	TLSKeyFile           string
	// OIDC signs people in on the monitoring page and the API. Devices keep
	// AuthToken.
	OIDCIssuer      string
	OIDCClientID    string
	OIDCRedirectURL string
}

// Load builds a Config from getenv, which is os.Getenv outside of tests.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		ListenAddr:       or(getenv("LISTEN_ADDR"), ":8080"),
		SwitchFrameURL:   getenv("SWITCHFRAME_URL"),
		SwitchFrameToken: getenv("SWITCHFRAME_TOKEN"),
		DataDir:          or(getenv("DATA_DIR"), "/data"),
		AuthToken:        getenv("AUTH_TOKEN"),
		TLSCertFile:      getenv("TLS_CERT_FILE"),
		TLSKeyFile:       getenv("TLS_KEY_FILE"),
		OIDCIssuer:       getenv("OIDC_ISSUER"),
		OIDCClientID:     getenv("OIDC_CLIENT_ID"),
		OIDCRedirectURL:  getenv("OIDC_REDIRECT_URL"),
	}
	var errs []error
	for _, d := range []struct {
		dst  *time.Duration
		name string
		def  time.Duration
	}{
		{&c.PollInterval, "POLL_INTERVAL", 50 * time.Millisecond},
		{&c.UpstreamStaleTimeout, "UPSTREAM_STALE_TIMEOUT", 3 * time.Second},
		{&c.HeartbeatInterval, "HEARTBEAT_INTERVAL", 10 * time.Second},
		{&c.ClientTimeout, "CLIENT_TIMEOUT", 30 * time.Second},
	} {
		*d.dst = d.def
		v := getenv(d.name)
		if v == "" {
			continue
		}
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive duration", d.name, v))
			continue
		}
		*d.dst = parsed
	}
	if c.SwitchFrameURL == "" {
		errs = append(errs, errors.New("SWITCHFRAME_URL is required"))
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		errs = append(errs, errors.New("TLS_CERT_FILE and TLS_KEY_FILE must be set together"))
	}
	if set := c.OIDCIssuer != "" || c.OIDCClientID != "" || c.OIDCRedirectURL != ""; set {
		if c.OIDCIssuer == "" || c.OIDCClientID == "" || c.OIDCRedirectURL == "" {
			errs = append(errs, errors.New("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_REDIRECT_URL must be set together"))
		} else if u, err := url.Parse(c.OIDCRedirectURL); err != nil || !u.IsAbs() || u.Path != "/auth/callback" {
			errs = append(errs, fmt.Errorf("OIDC_REDIRECT_URL: %q is not an absolute URL ending in /auth/callback", c.OIDCRedirectURL))
		}
	}
	return c, errors.Join(errs...)
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
