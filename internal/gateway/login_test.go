package gateway

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/marquies/dmf-app-tally-gateway/internal/config"
)

// fakeIdP is an OIDC provider that issues one code per authorization request
// and checks the PKCE verifier on the token request, as Keycloak does for a
// public client.
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey
	// audience the ID token is issued for; the client id when empty.
	audience string

	mu    sync.Mutex
	codes map[string]url.Values
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeIdP{t: t, key: key, codes: map[string]url.Values{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.srv.URL,
			"authorization_endpoint":                p.srv.URL + "/auth",
			"token_endpoint":                        p.srv.URL + "/token",
			"jwks_uri":                              p.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := randomID()
		p.mu.Lock()
		p.codes[code] = q
		p.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		auth, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || auth.Get("code_challenge_method") != "S256" ||
			auth.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) ||
			r.Form.Get("client_id") != auth.Get("client_id") ||
			r.Form.Get("redirect_uri") != auth.Get("redirect_uri") {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		aud := p.audience
		if aud == "" {
			aud = auth.Get("client_id")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 300,
			"id_token": p.idToken(aud),
		})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeIdP) idToken(aud string) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		p.t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: p.srv.URL, Subject: "operator", Audience: jwt.Audience{aud},
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Serialize()
	if err != nil {
		p.t.Fatal(err)
	}
	return raw
}

// The route's host, which the pod never sees: the identity provider sends
// the browser there, and the test carries the request over to the gateway.
const publicOrigin = "https://tally.example"

func newOIDCEnv(t *testing.T, idp *fakeIdP) *testEnv {
	return newEnv(t, func(c *config.Config) {
		c.AuthToken = "s3cret"
		c.OIDCIssuer = idp.srv.URL
		c.OIDCClientID = "switchframe.ns.sf"
		c.OIDCRedirectURL = publicOrigin + "/auth/callback"
	})
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func get(t *testing.T, rawURL string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", rawURL, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// authorize runs the browser's side of the sign-in and returns where the
// identity provider sent it back to, on the gateway.
func (e *testEnv) authorize(t *testing.T) string {
	t.Helper()
	resp := get(t, e.srv.URL+"/", nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / unsigned = %d, want a redirect to the identity provider", resp.StatusCode)
	}
	at, _ := url.Parse(resp.Header.Get("Location"))
	q := at.Query()
	if q.Get("client_id") != "switchframe.ns.sf" || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") != publicOrigin+"/auth/callback" {
		t.Fatalf("authorization request %s", at)
	}
	resp = get(t, at.String(), nil)
	back, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.HasPrefix(back.String(), publicOrigin+"/auth/callback?") {
		t.Fatalf("identity provider sent the browser to %s", back)
	}
	return e.srv.URL + back.RequestURI()
}

func (e *testEnv) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	resp := get(t, e.authorize(t), nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
				t.Errorf("session cookie %+v: the route serves https, and scripts and other sites stay off it", c)
			}
			return c
		}
	}
	t.Fatal("callback set no session cookie")
	return nil
}

func TestOIDCSignsPeopleInAndKeepsDevicesOnTheToken(t *testing.T) {
	e := newOIDCEnv(t, newFakeIdP(t))
	session := e.signIn(t)

	for _, path := range []string{"/", "/api/v1/devices", "/api/v1/sources"} {
		if resp := get(t, e.srv.URL+path, session); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s signed in = %d", path, resp.StatusCode)
		}
		// Scripts and the existing ?token= link keep working.
		if resp := e.request(t, "GET", path, ""); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s with token = %d", path, resp.StatusCode)
		}
	}
	// An API call is refused rather than redirected: a redirect would hand
	// the page's fetch the identity provider's login form as the response.
	if resp := get(t, e.srv.URL+"/api/v1/devices", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/devices unsigned = %d", resp.StatusCode)
	}
	// A device has no browser session to sign in with; a session alone does
	// not open the device protocol.
	if _, resp, err := e.dial(t, http.Header{"Cookie": {session.String()}}, ""); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("device with a session cookie and no token: err=%v resp=%v", err, resp)
	}
	d, _, err := e.dial(t, http.Header{"Authorization": {"Bearer s3cret"}}, "")
	if err != nil {
		t.Fatalf("device with token: %v", err)
	}
	d.conn.CloseNow()
}

func TestOIDCSessionExpires(t *testing.T) {
	e := newOIDCEnv(t, newFakeIdP(t))
	session := e.signIn(t)
	later := time.Now().Add(sessionTTL + time.Minute)
	e.g.login.now = func() time.Time { return later }
	if resp := get(t, e.srv.URL+"/api/v1/devices", session); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session = %d", resp.StatusCode)
	}
}

func TestOIDCCallbackRefuses(t *testing.T) {
	idp := newFakeIdP(t)
	e := newOIDCEnv(t, idp)

	// A callback this gateway did not begin, or one replayed, starts no
	// session: the state ties the code to the browser that asked for it.
	if resp := get(t, e.srv.URL+"/auth/callback?code=x&state=forged", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("forged state = %d", resp.StatusCode)
	}
	back := e.authorize(t)
	if resp := get(t, back, nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("first callback = %d", resp.StatusCode)
	}
	if resp := get(t, back, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("replayed callback = %d", resp.StatusCode)
	}

	// An ID token issued to another client of the realm is not a sign-in to
	// this booking.
	idp.audience = "another-client"
	resp := get(t, e.authorize(t), nil)
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Errorf("foreign audience = %d, cookies %v", resp.StatusCode, resp.Cookies())
	}
}

func TestWithoutOIDCTheTokenGuardsThePage(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AuthToken = "s3cret" })
	if resp := get(t, e.srv.URL+"/", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	if resp := get(t, e.srv.URL+"/auth/callback?code=x&state=y", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("callback without OIDC = %d", resp.StatusCode)
	}
}
