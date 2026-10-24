// proxy.go forwards requests to the dev server on localhost and keeps the visitor's view of it
// consistent: it rewrites the Host and Origin headers, redirects and cookies. It also holds the
// request log and the "device connected" notice, which wrap every handler in yonder.

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// clientHostKey is the context key under which the proxy stores the host name the visitor used,
// so redirects from the app can be pointed back at it.
type clientHostKey struct{}

// newProxy forwards to targetAddr while making the request look local,
// so dev servers that check Host or Origin accept it.
func newProxy(targetAddr string, port int) *httputil.ReverseProxy {
	local := fmt.Sprintf("localhost:%d", port)
	target := &url.URL{Scheme: "http", Host: targetAddr}
	return &httputil.ReverseProxy{
		// No SetXForwarded: frameworks compare X-Forwarded-Host with Origin.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = local
			// Only same-site Origins: rewriting others would defeat the dev server's CSRF checks.
			if pr.Out.Header.Get("Origin") == "http://"+pr.In.Host {
				pr.Out.Header.Set("Origin", "http://"+local)
			}
			pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), clientHostKey{}, pr.In.Host))
		},
		ModifyResponse: func(resp *http.Response) error {
			client, _ := resp.Request.Context().Value(clientHostKey{}).(string)
			rewriteLocation(resp.Header, port, client)
			stripLocalhostCookieDomain(resp.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			say(w, "yonder: localhost:%d is not responding\n", port)
		},
	}
}

// rewriteLocation points redirects to the local target back at the host the client used.
func rewriteLocation(h http.Header, port int, client string) {
	loc := h.Get("Location")
	if loc == "" || client == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil || u.Host == "" { // relative paths have no host; //host is scheme-relative
		return
	}
	match := false
	switch strings.ToLower(u.Host) {
	case fmt.Sprintf("localhost:%d", port), fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("[::1]:%d", port):
		match = true
	case "localhost", "127.0.0.1", "[::1]":
		// A bare host means the scheme's default port: 80 for http, but 443 for https, a different server.
		match = port == 80 && strings.EqualFold(u.Scheme, "http")
	}
	if match {
		u.Scheme, u.Host = "http", client
		h.Set("Location", u.String())
	}
}

// stripLocalhostCookieDomain removes Domain=localhost, which browsers reject on an IP host.
func stripLocalhostCookieDomain(h http.Header) {
	cookies := h["Set-Cookie"] // modified in place
	for i, c := range cookies {
		parts := strings.Split(c, ";")
		kept := parts[:0]
		for _, p := range parts {
			switch strings.ToLower(strings.TrimSpace(p)) {
			case "domain=localhost", "domain=.localhost":
				continue
			}
			kept = append(kept, strings.TrimSpace(p))
		}
		if len(kept) != len(parts) {
			cookies[i] = strings.Join(kept, "; ")
		}
	}
}

// withLog writes one line per request: time, method, path, status, client IP.
func withLog(h http.Handler, w io.Writer, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := now()
		rec := &statusRecorder{ResponseWriter: rw}
		// Deferred so a stream aborted with http.ErrAbortHandler still logs.
		defer func() {
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			u := *r.URL
			u.RawQuery = redactToken(u.RawQuery) // the share token must not reach the terminal log
			say(w, "%s  %s %s  %d  %s\n", start.Format("15:04:05"), r.Method, u.RequestURI(), rec.status, ip)
		}()
		h.ServeHTTP(rec, r)
	})
}

// statusRecorder wraps a ResponseWriter to remember the status code, which the request log
// prints. It passes everything else through, including WebSocket upgrades and file streaming.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the first status code the handler sends and then forwards it.
func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// ReadFrom keeps the OS fast path for file downloads. Wrapping a ResponseWriter hides its
// ReadFrom method, and http.ServeContent only uses sendfile when it finds one.
func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{s.ResponseWriter}, r) // hide ReadFrom to avoid recursing into ourselves
}

// Hijack is only called for protocol upgrades, which ReverseProxy answers with 101 on the raw conn.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if s.status == 0 {
		s.status = http.StatusSwitchingProtocols
	}
	return http.NewResponseController(s.ResponseWriter).Hijack()
}

// Unwrap lets http.ResponseController reach Flush on the real writer (SSE).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// newConnLogger prints one line the first time each client IP opens a connection,
// so a stalled request is still visible: the device reached yonder, even if nothing came back.
func newConnLogger(w io.Writer, now func() time.Time) func(net.Conn, http.ConnState) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(c net.Conn, s http.ConnState) {
		if s != http.StateNew {
			return
		}
		ip, _, err := net.SplitHostPort(c.RemoteAddr().String())
		if err != nil {
			ip = c.RemoteAddr().String()
		}
		mu.Lock()
		defer mu.Unlock()
		if seen[ip] {
			return
		}
		seen[ip] = true
		say(w, "%s  device connected  %s\n", now().Format("15:04:05"), ip)
	}
}
