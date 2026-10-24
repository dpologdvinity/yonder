// lan_test.go tests choosing the address that goes into the QR code.

package main

import (
	"net"
	"slices"
	"testing"
)

// ips parses addresses written as text, so test tables stay readable.
func ips(ss ...string) []net.IP {
	out := make([]net.IP, len(ss))
	for i, s := range ss {
		out[i] = net.ParseIP(s)
	}
	return out
}

// ipStrings turns addresses back into text, for comparing and for failure messages.
func ipStrings(in []net.IP) []string {
	out := make([]string, len(in))
	for i, ip := range in {
		out[i] = ip.String()
	}
	return out
}

// TestLanAddrs checks which addresses are offered and in what order, on machines shaped like
// real ones: WSL with a private address on loopback, Docker, VPNs, VM networks, and Windows
// interface names.
func TestLanAddrs(t *testing.T) {
	// Modelled on WSL mirrored mode: a private address on lo, plus docker0.
	ifs := []iface{
		{"lo", net.FlagUp | net.FlagLoopback, ips("127.0.0.1", "10.255.255.254")},
		{"docker0", net.FlagUp, ips("172.17.0.1")},
		{"eth1", net.FlagUp, ips("10.207.68.227", "fe80::1")},
		{"wlan0", 0, ips("192.168.1.5")},        // down
		{"tun0", net.FlagUp, ips("100.64.0.2")}, // CGNAT, not private
	}

	tests := []struct {
		name string
		ifs  []iface
		best net.IP
		want []string
	}{
		{"best is primary", ifs, net.ParseIP("10.207.68.227"), []string{"10.207.68.227", "172.17.0.1"}},
		{"no best", ifs, nil, []string{"10.207.68.227", "172.17.0.1"}},
		{"best is bridge", ifs, net.ParseIP("172.17.0.1"), []string{"10.207.68.227", "172.17.0.1"}},
		{"best is full-tunnel VPN", []iface{
			{"tun0", net.FlagUp, ips("10.8.0.2")},
			{"eth0", net.FlagUp, ips("192.168.1.5")},
			{"vboxnet0", net.FlagUp, ips("192.168.56.1")},
		}, net.ParseIP("10.8.0.2"), []string{"192.168.1.5", "10.8.0.2", "192.168.56.1"}},
		{"windows names", []iface{
			{"Ethernet", net.FlagUp, ips("192.168.1.5")},
			{"OpenVPN TAP-Windows6", net.FlagUp, ips("10.8.0.2")},
			{"vEthernet (WSL)", net.FlagUp, ips("172.30.1.1")},
			{"WireGuard Tunnel", net.FlagUp, ips("10.9.0.2")},
		}, net.ParseIP("10.8.0.2"), []string{"192.168.1.5", "10.8.0.2", "172.30.1.1", "10.9.0.2"}},
		{"point-to-point is a tunnel", []iface{
			{"Wi-Fi", net.FlagUp, ips("192.168.1.5")},
			{"corp", net.FlagUp | net.FlagPointToPoint, ips("10.1.1.1")},
		}, net.ParseIP("10.1.1.1"), []string{"192.168.1.5", "10.1.1.1"}},
		{"best is the second normal interface", []iface{
			{"eth0", net.FlagUp, ips("192.168.50.5")},
			{"wlan0", net.FlagUp, ips("192.168.1.5")},
		}, net.ParseIP("192.168.1.5"), []string{"192.168.1.5", "192.168.50.5"}},
		{"best filtered out", ifs, net.ParseIP("10.255.255.254"), []string{"10.207.68.227", "172.17.0.1"}},
		{"no interfaces", nil, nil, []string{}},
		{"duplicate address", []iface{
			{"eth0", net.FlagUp, ips("192.168.1.5")},
			{"eth0:1", net.FlagUp, ips("192.168.1.5", "192.168.1.6")},
		}, nil, []string{"192.168.1.5", "192.168.1.6"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipStrings(lanAddrs(tt.ifs, tt.best))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSystemLANAddrs checks the real network lookup on whatever machine runs the tests: it
// must not fail, and every address it offers must be a private IPv4 address.
func TestSystemLANAddrs(t *testing.T) {
	addrs, err := systemLANAddrs()
	if err != nil {
		t.Fatalf("systemLANAddrs: %v", err)
	}
	for _, a := range addrs {
		if a.To4() == nil || !a.IsPrivate() {
			t.Errorf("offered address %v is not a private IPv4 address", a)
		}
	}
}

// TestDefaultRouteIP checks that the default-route lookup returns either nothing (no
// network) or a usable address, and never panics.
func TestDefaultRouteIP(t *testing.T) {
	if ip := defaultRouteIP(); ip != nil && ip.IsUnspecified() {
		t.Errorf("defaultRouteIP = %v, want nil or a real address", ip)
	}
}
