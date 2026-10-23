package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const tokenParam = "yonder"

// newToken returns 128 random bits, URL-safe. A new token every run cuts off old links.
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// withAuth lets a request through only with the share token, from the query
// (first visit, via the QR code) or from the cookie set on that visit.
func withAuth(h http.Handler, token string, listenPort int) http.Handler {
	cookieName := fmt.Sprintf("yonder_%d", listenPort)
	valid := func(s string) bool { return subtle.ConstantTimeCompare([]byte(s), []byte(token)) == 1 }

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaQuery := valid(r.URL.Query().Get(tokenParam))
		c, err := r.Cookie(cookieName)
		viaCookie := err == nil && valid(c.Value)
		if !viaQuery && !viaCookie {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "yonder: this link needs the access token. Scan the QR code from the person sharing.\n")
			return
		}

		// Never pass yonder's own parameter or cookie on to the app.
		out := r.Clone(r.Context())
		out.URL.RawQuery = dropParam(r.URL.RawQuery, tokenParam)
		out.Header.Del("Cookie")
		if rest := dropCookie(r.Header.Values("Cookie")); rest != "" {
			out.Header.Set("Cookie", rest)
		}

		if viaQuery {
			// The token is still in this page's address bar until a redirect; keep it out of Referer.
			w.Header().Set("Referrer-Policy", "no-referrer")
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				// One leading slash only: "//host" would be an open redirect.
				// Built from Path and RawQuery only: RequestURI() would also include Opaque.
				target := url.URL{Path: "/" + strings.TrimLeft(out.URL.Path, "/"), RawQuery: out.URL.RawQuery}
				http.Redirect(w, r, target.RequestURI(), http.StatusFound)
				return
			}
		}
		h.ServeHTTP(w, out)
	})
}

// dropParam removes every name=value pair called name, keeping the others in order.
func dropParam(rawQuery, name string) string {
	var kept []string
	for _, part := range strings.Split(rawQuery, "&") {
		key, _, _ := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(key); part == "" || (err == nil && k == name) {
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(kept, "&")
}

// dropCookie removes yonder's own cookies (yonder_<port>, from this or any other
// instance) from raw Cookie header values, keeping every other pair byte for byte.
// r.Cookies() would drop values with quotes or UTF-8.
func dropCookie(headers []string) string {
	var kept []string
	for _, h := range headers {
		for _, part := range strings.Split(h, ";") {
			part = strings.TrimSpace(part)
			if k, _, _ := strings.Cut(part, "="); part == "" || isYonderCookie(strings.TrimSpace(k)) {
				continue
			}
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

// isYonderCookie matches "yonder_" followed only by digits, so an app's own
// "yonder_theme" survives.
func isYonderCookie(name string) bool {
	digits, ok := strings.CutPrefix(name, "yonder_")
	if !ok || digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// redactParam replaces the value of every name=value pair called name with "…", for logs.
func redactParam(rawQuery, name string) string {
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		key, _, found := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(key); found && err == nil && k == name {
			parts[i] = key + "=…"
		}
	}
	return strings.Join(parts, "&")
}
