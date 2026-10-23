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
		if rest := dropCookie(r.Header.Values("Cookie"), cookieName); rest != "" {
			out.Header.Set("Cookie", rest)
		}

		if viaQuery {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, out.URL.RequestURI(), http.StatusFound)
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

// dropCookie removes the named cookie from raw Cookie header values, keeping every
// other pair byte for byte. r.Cookies() would drop values with quotes or UTF-8.
func dropCookie(headers []string, name string) string {
	var kept []string
	for _, h := range headers {
		for _, part := range strings.Split(h, ";") {
			part = strings.TrimSpace(part)
			if k, _, _ := strings.Cut(part, "="); part == "" || strings.TrimSpace(k) == name {
				continue
			}
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}
