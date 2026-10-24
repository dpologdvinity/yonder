// auth.go is the access gate. Every request must carry the secret share token, either in the
// link's query string on the first visit or in the cookie that visit set. Requests without it
// get 403 and never reach the app. The gate also strips its own token and cookie before the
// request moves on, and keeps the token out of Referer headers and the log.

package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// tokenParam is the name of the query parameter that carries the share token ("?yonder=...").
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
	return withAuthDevices(h, token, listenPort, nil)
}

// withAuthDevices is withAuth with optional device approval. With a non-nil devices table, a
// valid token is not enough: the first visit from a browser creates a device that the owner
// must approve at the terminal, and the cookie then holds "<token>.<device id>". Until the
// owner answers, the device sees a waiting page; if refused, a 403.
func withAuthDevices(h http.Handler, token string, listenPort int, devices *deviceTable) http.Handler {
	cookieName := fmt.Sprintf("yonder_%d", listenPort)
	valid := func(s string) bool { return subtle.ConstantTimeCompare([]byte(s), []byte(token)) == 1 }

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaQuery := valid(r.URL.Query().Get(tokenParam))
		viaCookie, deviceID := false, ""
		if c, err := r.Cookie(cookieName); err == nil {
			viaCookie, deviceID = checkCookie(c.Value, valid, devices)
		}
		if !viaQuery && !viaCookie {
			refuse(w, http.StatusForbidden, "yonder: this link needs the access token. Scan the QR code from the person sharing.\n")
			return
		}

		// Never pass yonder's own parameter or cookie on to the app.
		out := r.Clone(r.Context())
		out.URL.RawQuery = dropParam(r.URL.RawQuery, tokenParam)
		out.Header.Del("Cookie")
		if rest := dropCookie(r.Header.Values("Cookie")); rest != "" {
			out.Header.Set("Cookie", rest)
		}

		newDevice := false
		if devices != nil && !viaCookie { // the link proves the token; this browser is new
			id, ok := devices.beginFrom(r.RemoteAddr, r.UserAgent(), requestCountry(r))
			if !ok {
				refuse(w, http.StatusTooManyRequests, "yonder: Too many devices are waiting for approval. Try again in a moment.\n")
				return
			}
			deviceID, newDevice = id, true
		}
		if viaQuery {
			// The token is still in this page's address bar until a redirect; keep it out of Referer.
			w.Header().Set("Referrer-Policy", "no-referrer")
			if devices == nil || newDevice {
				value := token
				if devices != nil {
					value = token + "." + deviceID
				}
				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteLaxMode})
			}
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				// One leading slash only: "//host" would be an open redirect.
				// Built from Path and RawQuery only: RequestURI() would also include Opaque.
				target := "/" + strings.TrimLeft(out.URL.EscapedPath(), "/") // keeps %2F as the app sent it
				if out.URL.RawQuery != "" {
					target += "?" + out.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusFound)
				return
			}
		}
		if devices != nil {
			switch st, _ := devices.state(deviceID); st {
			case devPending:
				devices.touch(deviceID) // it is still there: keep its place
				markQuiet(r)            // it reloads every two seconds: do not bury the owner's question
				waitingPage(w)
				return
			case devDenied:
				refuse(w, http.StatusForbidden, "yonder: The owner did not allow this device.\n")
				return
			}
		}
		if viaQuery {
			w = noReferrerWriter{w} // a POST keeps the token in the address bar
		}
		h.ServeHTTP(w, out)
	})
}

// checkCookie reports whether the yonder cookie's value grants access, and for which device.
// Without a device table the value is the token itself. With one it is "<token>.<device id>"
// and the id must be one this run has seen.
func checkCookie(value string, valid func(string) bool, devices *deviceTable) (ok bool, deviceID string) {
	if devices == nil {
		return valid(value), ""
	}
	tok, id, found := strings.Cut(value, ".")
	if !found || !valid(tok) {
		return false, ""
	}
	if _, known := devices.state(id); !known {
		return false, ""
	}
	return true, id
}

// refuse answers with a plain-text error page.
func refuse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	say(w, "%s", message)
}

// waitingPage answers a device that the owner has not approved yet: 202 and a small page that
// reloads itself every two seconds, so the visitor lands on the site once it is approved.
func waitingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(http.StatusAccepted)
	say(w, "%s", `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="refresh" content="2"><title>Waiting for approval</title></head><body style="font:16px system-ui,sans-serif;margin:2rem"><h1>Waiting for approval</h1><p>The person sharing has to approve this device on their computer. This page reloads by itself.</p></body></html>`)
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

// looseTokenRe finds a "yonder=..." pair even in a malformed query that Go's parser
// would reject (for example "a;yonder=T"). Such a request never authenticates, but its
// text must still stay out of the log.
var looseTokenRe = regexp.MustCompile(`(?i)(^|[&;])` + tokenParam + `=[^&;]*`)

// redactToken returns rawQuery without any share token, for the terminal log.
func redactToken(rawQuery string) string {
	q := dropParam(rawQuery, tokenParam)
	q = looseTokenRe.ReplaceAllString(q, "$1")
	return strings.Trim(strings.ReplaceAll(q, "&&", "&"), "&;")
}

// noReferrerWriter forces "Referrer-Policy: no-referrer" on the response, replacing
// any policy the app sets, so the token in the address bar cannot leak through Referer.
type noReferrerWriter struct{ http.ResponseWriter }

// WriteHeader sets the Referrer-Policy header just before the status line goes out, so it
// replaces any policy the app chose.
func (w noReferrerWriter) WriteHeader(code int) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.ResponseWriter.WriteHeader(code)
}

// Write sets the Referrer-Policy header before the first body bytes, for handlers that never
// call WriteHeader.
func (w noReferrerWriter) Write(b []byte) (int, error) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush and Hijack on the real writer.
func (w noReferrerWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
