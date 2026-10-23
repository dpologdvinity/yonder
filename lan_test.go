package main

import (
	"net"
	"slices"
	"testing"
)

func ips(ss ...string) []net.IP {
	out := make([]net.IP, len(ss))
	for i, s := range ss {
		out[i] = net.ParseIP(s)
	}
	return out
}

func ipStrings(in []net.IP) []string {
	out := make([]string, len(in))
	for i, ip := range in {
		out[i] = ip.String()
	}
	return out
}

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
		{"best is bridge", ifs, net.ParseIP("172.17.0.1"), []string{"172.17.0.1", "10.207.68.227"}},
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
