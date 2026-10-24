// auth_test.go tests the access gate in auth.go: who gets in, what the app sees afterwards, and
// what must never leak.

package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wantForbidden is the exact body of the 403 response.
const wantForbidden = "yonder: this link needs the access token. Scan the QR code from the person sharing.\n"

// wantAuthCookie is the exact Set-Cookie value the gate sets after a valid token.
const wantAuthCookie = "yonder_18000=T0K; Path=/; HttpOnly; SameSite=Lax"

// recorder is a stand-in app that remembers whether it was called and which request it got.
type recorder struct {
	called bool
	req    *http.Request
}

// ServeHTTP records the request, so tests can inspect what the gate let through.
func (u *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) { u.called, u.req = true, r }

// serveAuth sends one request through the gate, in front of a recorder, and returns the gate's
// response and the recorder.
func serveAuth(method, target string, hdr http.Header) (*httptest.ResponseRecorder, *recorder) {
	up := &recorder{}
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	withAuth(up, "T0K", 18000).ServeHTTP(rec, req)
	return rec, up
}

// TestNewToken checks that tokens are 22 characters (16 random bytes), URL-safe, and different
// every time.
func TestNewToken(t *testing.T) {
	a, b := newToken(), newToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	for _, tok := range []string{a, b} {
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if len(tok) != 22 || err != nil || len(raw) != 16 {
			t.Errorf("token %q: len %d, decoded %d bytes, err %v; want 22 chars, 16 bytes", tok, len(tok), len(raw), err)
		}
	}
}

// assertForbidden fails the test unless the response is the 403 page and the app was never
// called.
func assertForbidden(t *testing.T, rec *httptest.ResponseRecorder, up *recorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || rec.Body.String() != wantForbidden {
		t.Errorf("got %d %q, want 403 %q", rec.Code, rec.Body.String(), wantForbidden)
	}
	if up.called {
		t.Error("upstream was called")
	}
}

// TestAuthNoToken checks that a request without a token gets 403 and never reaches the app.
func TestAuthNoToken(t *testing.T) {
	rec, up := serveAuth("GET", "/", nil)
	assertForbidden(t, rec, up)
}

// TestAuthWrongToken checks that a wrong token in the query, or a stale cookie from an earlier
// run, gets 403.
func TestAuthWrongToken(t *testing.T) {
	rec, up := serveAuth("GET", "/?yonder=nope", nil)
	assertForbidden(t, rec, up)
	rec, up = serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=OLD"}})
	assertForbidden(t, rec, up)
}

// TestAuthQueryTokenRedirects checks that a GET or HEAD with the right token sets the cookie
// and redirects to the same address without the token, keeping the other parameters in order.
func TestAuthQueryTokenRedirects(t *testing.T) {
	tests := []struct{ method, target, location string }{
		{"GET", "/?yonder=T0K", "/"},
		{"GET", "/app?a=1&yonder=T0K", "/app?a=1"},
		{"GET", "/app?b=2&yonder=T0K&a=1", "/app?b=2&a=1"},
		{"HEAD", "/?yonder=T0K", "/"},
	}
	for _, tt := range tests {
		rec, up := serveAuth(tt.method, tt.target, nil)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != tt.location {
			t.Errorf("%s %s: got %d Location %q, want 302 %q", tt.method, tt.target, rec.Code, rec.Header().Get("Location"), tt.location)
		}
		if got := rec.Header().Get("Set-Cookie"); got != wantAuthCookie {
			t.Errorf("%s %s: Set-Cookie %q, want %q", tt.method, tt.target, got, wantAuthCookie)
		}
		if up.called {
			t.Errorf("%s %s: upstream was called", tt.method, tt.target)
		}
	}
}

// TestAuthQueryTokenPostForwards checks that a POST with the right token is forwarded (a
// redirect would lose its body) without the token, and still sets the cookie.
func TestAuthQueryTokenPostForwards(t *testing.T) {
	rec, up := serveAuth("POST", "/api?yonder=T0K&x=2", nil)
	if !up.called || up.req.URL.RawQuery != "x=2" {
		t.Fatalf("upstream called=%v query=%q, want called with x=2", up.called, up.req.URL.RawQuery)
	}
	if got := rec.Header().Get("Set-Cookie"); got != wantAuthCookie {
		t.Errorf("Set-Cookie %q, want %q", got, wantAuthCookie)
	}
}

// TestAuthCookiePasses checks that a valid cookie lets the request through and that the app
// sees only its own cookies.
func TestAuthCookiePasses(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=T0K; session=abc"}})
	if !up.called || up.req.Header.Get("Cookie") != "session=abc" {
		t.Fatalf("upstream called=%v Cookie=%q, want session=abc", up.called, up.req.Header.Get("Cookie"))
	}
	_, up = serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=T0K"}})
	if _, ok := up.req.Header["Cookie"]; ok {
		t.Errorf("upstream Cookie header = %q, want none", up.req.Header.Get("Cookie"))
	}
}

// TestAuthCookieWithBadParam checks that a bad token in the query does not block a request that
// already has a valid cookie, and is not passed on.
func TestAuthCookieWithBadParam(t *testing.T) {
	_, up := serveAuth("GET", "/?yonder=nope&y=1", http.Header{"Cookie": {"yonder_18000=T0K"}})
	if !up.called || up.req.URL.RawQuery != "y=1" {
		t.Fatalf("upstream called=%v query=%q, want called with y=1", up.called, up.req.URL.RawQuery)
	}
}

// TestAuthUpgradeWithoutCookie checks that a WebSocket upgrade without the cookie gets 403 and
// never reaches the app.
func TestAuthUpgradeWithoutCookie(t *testing.T) {
	rec, up := serveAuth("GET", "/", http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}})
	assertForbidden(t, rec, up)
}

// TestAuthKeepsRawAppCookies checks that cookie values with quotes, JSON or non-ASCII text
// reach the app byte for byte.
func TestAuthKeepsRawAppCookies(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {`yonder_18000=T0K; j={"a":1}; q="v"; u=café; e=`}})
	if want := `j={"a":1}; q="v"; u=café; e=`; !up.called || up.req.Header.Get("Cookie") != want {
		t.Fatalf("upstream Cookie = %q, want %q", up.req.Header.Get("Cookie"), want)
	}
}

// TestAuthRedirectStaysOnSite checks that the redirect target never starts with two slashes, so
// a crafted link cannot send a visitor to another website.
func TestAuthRedirectStaysOnSite(t *testing.T) {
	for _, target := range []string{"//evil.com/?yonder=T0K", "///evil.com/x?yonder=T0K"} {
		rec, _ := serveAuth("GET", target, nil)
		loc := rec.Header().Get("Location")
		if strings.HasPrefix(loc, "//") || !strings.HasPrefix(loc, "/") {
			t.Errorf("%s: Location %q can leave the site", target, loc)
		}
	}
}

// TestAuthStripsEveryYonderCookie checks that cookies of other yonder instances
// (yonder_<digits>) are removed before the app sees the request.
func TestAuthStripsEveryYonderCookie(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {"yonder_13000=OTHER; yonder_18000=T0K; session=abc"}})
	if !up.called || up.req.Header.Get("Cookie") != "session=abc" {
		t.Fatalf("upstream called=%v Cookie=%q, want session=abc only", up.called, up.req.Header.Get("Cookie"))
	}
}

// TestAuthKeepsAppCookiesNamedLikeYonder checks that an app cookie such as yonder_theme is not
// mistaken for a yonder cookie and still reaches the app.
func TestAuthKeepsAppCookiesNamedLikeYonder(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=T0K; yonder_theme=dark; yonder_9=old"}})
	if got := up.req.Header.Get("Cookie"); got != "yonder_theme=dark" {
		t.Errorf("upstream Cookie = %q, want yonder_theme=dark (only yonder_<digits> stripped)", got)
	}
}

// TestAuthTokenRequestsSendNoReferrer checks that a request carrying the token gets Referrer-
// Policy: no-referrer, so the token cannot leak in a Referer header.
func TestAuthTokenRequestsSendNoReferrer(t *testing.T) {
	rec, _ := serveAuth("POST", "/api?yonder=T0K", nil)
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

// TestAuthRedirectIgnoresOpaqueTarget checks that a request whose URL has an opaque part (a
// malformed absolute-form target) cannot become a redirect to another website.
func TestAuthRedirectIgnoresOpaqueTarget(t *testing.T) {
	req := httptest.NewRequest("GET", "/?yonder=T0K", nil)
	req.URL.Opaque = `\evil.com`
	rec := httptest.NewRecorder()
	withAuth(&recorder{}, "T0K", 18000).ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "evil") || !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "/\\") {
		t.Errorf("Location = %q", loc)
	}
}

// TestAuthRedirectKeepsEncodedPath checks that an encoded slash (%2F) in the path survives the
// redirect exactly as the app expects it.
func TestAuthRedirectKeepsEncodedPath(t *testing.T) {
	rec, _ := serveAuth("GET", "/a%2Fb?yonder=T0K&x=1", nil)
	if got := rec.Header().Get("Location"); got != "/a%2Fb?x=1" {
		t.Errorf("Location = %q, want /a%%2Fb?x=1", got)
	}
}

// TestAuthNoReferrerBeatsUpstreamPolicy checks that even when the app sets its own Referrer-
// Policy, the response carries only no-referrer.
func TestAuthNoReferrerBeatsUpstreamPolicy(t *testing.T) {
	up := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "unsafe-url")
		_, _ = w.Write([]byte("ok"))
	})
	req := httptest.NewRequest("POST", "/api?yonder=T0K", nil)
	rec := httptest.NewRecorder()
	withAuth(up, "T0K", 18000).ServeHTTP(rec, req)
	if got := rec.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want only no-referrer", got)
	}
}
