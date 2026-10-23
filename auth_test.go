package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wantForbidden = "yonder: this link needs the access token. Scan the QR code from the person sharing.\n"
const wantAuthCookie = "yonder_18000=T0K; Path=/; HttpOnly; SameSite=Lax"

type recorder struct {
	called bool
	req    *http.Request
}

func (u *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) { u.called, u.req = true, r }

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

func assertForbidden(t *testing.T, rec *httptest.ResponseRecorder, up *recorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || rec.Body.String() != wantForbidden {
		t.Errorf("got %d %q, want 403 %q", rec.Code, rec.Body.String(), wantForbidden)
	}
	if up.called {
		t.Error("upstream was called")
	}
}

func TestAuthNoToken(t *testing.T) {
	rec, up := serveAuth("GET", "/", nil)
	assertForbidden(t, rec, up)
}

func TestAuthWrongToken(t *testing.T) {
	rec, up := serveAuth("GET", "/?yonder=nope", nil)
	assertForbidden(t, rec, up)
	rec, up = serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=OLD"}})
	assertForbidden(t, rec, up)
}

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

func TestAuthQueryTokenPostForwards(t *testing.T) {
	rec, up := serveAuth("POST", "/api?yonder=T0K&x=2", nil)
	if !up.called || up.req.URL.RawQuery != "x=2" {
		t.Fatalf("upstream called=%v query=%q, want called with x=2", up.called, up.req.URL.RawQuery)
	}
	if got := rec.Header().Get("Set-Cookie"); got != wantAuthCookie {
		t.Errorf("Set-Cookie %q, want %q", got, wantAuthCookie)
	}
}

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

func TestAuthCookieWithBadParam(t *testing.T) {
	_, up := serveAuth("GET", "/?yonder=nope&y=1", http.Header{"Cookie": {"yonder_18000=T0K"}})
	if !up.called || up.req.URL.RawQuery != "y=1" {
		t.Fatalf("upstream called=%v query=%q, want called with y=1", up.called, up.req.URL.RawQuery)
	}
}

func TestAuthUpgradeWithoutCookie(t *testing.T) {
	rec, up := serveAuth("GET", "/", http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}})
	assertForbidden(t, rec, up)
}

func TestAuthKeepsRawAppCookies(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {`yonder_18000=T0K; j={"a":1}; q="v"; u=café; e=`}})
	if want := `j={"a":1}; q="v"; u=café; e=`; !up.called || up.req.Header.Get("Cookie") != want {
		t.Fatalf("upstream Cookie = %q, want %q", up.req.Header.Get("Cookie"), want)
	}
}

func TestAuthRedirectStaysOnSite(t *testing.T) {
	for _, target := range []string{"//evil.com/?yonder=T0K", "///evil.com/x?yonder=T0K"} {
		rec, _ := serveAuth("GET", target, nil)
		loc := rec.Header().Get("Location")
		if strings.HasPrefix(loc, "//") || !strings.HasPrefix(loc, "/") {
			t.Errorf("%s: Location %q can leave the site", target, loc)
		}
	}
}

func TestAuthStripsEveryYonderCookie(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {"yonder_13000=OTHER; yonder_18000=T0K; session=abc"}})
	if !up.called || up.req.Header.Get("Cookie") != "session=abc" {
		t.Fatalf("upstream called=%v Cookie=%q, want session=abc only", up.called, up.req.Header.Get("Cookie"))
	}
}

func TestAuthKeepsAppCookiesNamedLikeYonder(t *testing.T) {
	_, up := serveAuth("GET", "/", http.Header{"Cookie": {"yonder_18000=T0K; yonder_theme=dark; yonder_9=old"}})
	if got := up.req.Header.Get("Cookie"); got != "yonder_theme=dark" {
		t.Errorf("upstream Cookie = %q, want yonder_theme=dark (only yonder_<digits> stripped)", got)
	}
}

func TestAuthTokenRequestsSendNoReferrer(t *testing.T) {
	rec, _ := serveAuth("POST", "/api?yonder=T0K", nil)
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestAuthRedirectIgnoresOpaqueTarget(t *testing.T) {
	req := httptest.NewRequest("GET", "/?yonder=T0K", nil)
	req.URL.Opaque = `\evil.com`
	rec := httptest.NewRecorder()
	withAuth(&recorder{}, "T0K", 18000).ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "evil") || !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "/\\") {
		t.Errorf("Location = %q", loc)
	}
}
