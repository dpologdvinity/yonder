// edge.go decides which request headers can be believed. Behind a tunnel, every request arrives
// from the tunnel program on this computer, and the visitor's real address is in a header the
// tunnel adds. A visitor on the local network could send the same header, so trust is decided
// by which listener a request came in on, never by the header: the tunnel listener believes
// the Cloudflare headers, and the network listener throws them away.

package main

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// stripProxyHeaders removes every header that names the visitor's address, country or
// connection scheme as seen by a proxy: the Cloudflare ones (CF-...), the forwarding ones
// (Forwarded, X-Forwarded-...), X-Real-IP and True-Client-IP. Nothing in yonder or in the app
// should act on them, because anyone can send them.
func stripProxyHeaders(h http.Header) {
	for name := range h {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "cf-") || strings.HasPrefix(lower, "x-forwarded-") ||
			lower == "forwarded" || lower == "x-real-ip" || lower == "true-client-ip" {
			delete(h, name)
		}
	}
}

// edgeKey is the context key under which the edge middleware stores what it learned about a
// request.
type edgeKey struct{}

// edgeInfo is what the tunnel listener learned about a request: that it came over https, and
// the visitor's country as reported by Cloudflare (empty when unknown).
type edgeInfo struct {
	secure  bool
	country string
}

// requestIsSecure reports whether the request reached yonder over https, which is only true
// for requests that came through the tunnel listener.
func requestIsSecure(r *http.Request) bool {
	info, _ := r.Context().Value(edgeKey{}).(edgeInfo)
	return info.secure
}

// requestCountry returns the visitor's two-letter country code as reported by the tunnel, or
// "" when there is none.
func requestCountry(r *http.Request) string {
	info, _ := r.Context().Value(edgeKey{}).(edgeInfo)
	return info.country
}

// withLANEdge wraps the handler of the listener that faces the local network. It removes all
// proxy headers, so a visitor cannot pass as someone else or as a secure tunnel visitor.
func withLANEdge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripProxyHeaders(r.Header)
		next.ServeHTTP(w, r)
	})
}

// countryRe matches the country values Cloudflare sends: two capital letters (XX means
// unknown) or T1 for the Tor network.
var countryRe = regexp.MustCompile(`^([A-Z]{2}|T1)$`)

// withTunnelEdge wraps the handler of the loopback listener that only the tunnel program
// connects to. It takes the visitor's address from CF-Connecting-IP (only if it is a single,
// valid IP address) and the country from CF-IPCountry (only if it looks like one), marks the
// request as secure, and removes all proxy headers before the request goes on.
func withTunnelEdge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP")))
		country := r.Header.Get("CF-IPCountry")
		stripProxyHeaders(r.Header)
		if !countryRe.MatchString(country) {
			country = ""
		}
		r = r.WithContext(context.WithValue(r.Context(), edgeKey{}, edgeInfo{secure: true, country: country}))
		if ip != nil {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0") // the port is unknown; callers only need the host
		}
		next.ServeHTTP(w, r)
	})
}

// addrGroup names the visitor that an address belongs to, for the limit on waiting devices per
// visitor. IPv4 addresses stand for themselves. An IPv6 address stands for its whole /64,
// because one household or phone usually controls all of it and could otherwise take a
// separate place with every address. Text that is not an address is returned as it is.
func addrGroup(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
