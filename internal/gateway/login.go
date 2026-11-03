package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "tally_session"
	sessionTTL    = 12 * time.Hour
	// How long a person has to complete the sign-in at the identity provider.
	loginTTL = 10 * time.Minute
)

// login signs people in with the OIDC authorization code flow and PKCE, as a
// public client, and remembers who signed in in a session cookie. Sessions
// are held in memory: there is one instance, and after a restart the
// identity provider's own session signs a person back in without a prompt.
type login struct {
	issuer   string
	clientID string
	redirect string
	secure   bool
	client   *http.Client
	now      func() time.Time

	mu       sync.Mutex
	provider *oidc.Provider
	pending  map[string]pendingLogin
	sessions map[string]time.Time
}

type pendingLogin struct {
	verifier string
	expires  time.Time
}

func newLogin(issuer, clientID, redirect string) *login {
	u, _ := url.Parse(redirect)
	return &login{
		issuer:   issuer,
		clientID: clientID,
		redirect: redirect,
		secure:   u.Scheme == "https",
		client:   &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
		pending:  map[string]pendingLogin{},
		sessions: map[string]time.Time{},
	}
}

// config discovers the provider on first use rather than at start, so the
// gateway serves devices while the identity provider is unreachable.
func (l *login) config(r *http.Request) (*oauth2.Config, *oidc.Provider, error) {
	l.mu.Lock()
	p := l.provider
	l.mu.Unlock()
	if p == nil {
		var err error
		p, err = oidc.NewProvider(oidc.ClientContext(r.Context(), l.client), l.issuer)
		if err != nil {
			return nil, nil, err
		}
		l.mu.Lock()
		l.provider = p
		l.mu.Unlock()
	}
	endpoint := p.Endpoint()
	// A public client names itself in the form. Detecting the style instead
	// tries Basic first and retries with a code the first attempt used up.
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return &oauth2.Config{
		ClientID:    l.clientID,
		Endpoint:    endpoint,
		RedirectURL: l.redirect,
		Scopes:      []string{oidc.ScopeOpenID},
	}, p, nil
}

func (l *login) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	expires, ok := l.sessions[c.Value]
	return ok && l.now().Before(expires)
}

// begin sends the browser to the identity provider.
func (l *login) begin(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := l.config(r)
	if err != nil {
		http.Error(w, "identity provider unreachable", http.StatusBadGateway)
		return
	}
	state, verifier := randomID(), oauth2.GenerateVerifier()
	now := l.now()
	l.mu.Lock()
	for s, p := range l.pending {
		if now.After(p.expires) {
			delete(l.pending, s)
		}
	}
	l.pending[state] = pendingLogin{verifier: verifier, expires: now.Add(loginTTL)}
	l.mu.Unlock()
	http.Redirect(w, r, cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// callback completes a sign-in begun here and starts a session.
func (l *login) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	l.mu.Lock()
	p, ok := l.pending[q.Get("state")]
	delete(l.pending, q.Get("state"))
	l.mu.Unlock()
	if !ok || l.now().After(p.expires) {
		http.Error(w, "unknown or expired sign-in, open the page again", http.StatusBadRequest)
		return
	}
	if e := q.Get("error"); e != "" {
		http.Error(w, "sign-in refused: "+e, http.StatusForbidden)
		return
	}
	cfg, provider, err := l.config(r)
	if err != nil {
		http.Error(w, "identity provider unreachable", http.StatusBadGateway)
		return
	}
	ctx := oidc.ClientContext(r.Context(), l.client)
	tok, err := cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(p.verifier))
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusForbidden)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	if _, err := provider.Verifier(&oidc.Config{ClientID: l.clientID}).Verify(ctx, raw); err != nil {
		http.Error(w, "sign-in failed", http.StatusForbidden)
		return
	}

	id, now := randomID(), l.now()
	l.mu.Lock()
	for s, expires := range l.sessions {
		if now.After(expires) {
			delete(l.sessions, s)
		}
	}
	l.sessions[id] = now.Add(sessionTTL)
	l.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   l.secure,
		// Lax keeps the cookie off cross-site PUT and DELETE requests.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func randomID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
