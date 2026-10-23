package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

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
			if pr.Out.Header.Get("Origin") != "" {
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
			fmt.Fprintf(w, "yonder: localhost:%d is not responding\n", port)
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
	if err != nil || !u.IsAbs() {
		return
	}
	switch u.Host {
	case fmt.Sprintf("localhost:%d", port), fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("[::1]:%d", port):
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
