package main

import (
	"net"
	"strings"
)

type iface struct {
	Name  string
	Flags net.Flags
	IPs   []net.IP
}

// lanAddrs returns private IPv4 addresses other devices may reach, best first,
// then normal interfaces, then container bridges.
func lanAddrs(ifaces []iface, best net.IP) []net.IP {
	var normal, bridge []net.IP
	bestFound := false
	seen := map[string]bool{}
	for _, ifc := range ifaces {
		// Skip by interface flag, not address: WSL mirrored mode puts 10.255.255.254 on lo.
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		isBridge := strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-") || strings.HasPrefix(ifc.Name, "veth")
		for _, ip := range ifc.IPs {
			ip4 := ip.To4()
			if ip4 == nil || !ip4.IsPrivate() || seen[ip4.String()] {
				continue
			}
			seen[ip4.String()] = true
			if best != nil && ip4.Equal(best) {
				bestFound = true
				continue
			}
			if isBridge {
				bridge = append(bridge, ip4)
			} else {
				normal = append(normal, ip4)
			}
		}
	}
	out := []net.IP{}
	if bestFound {
		out = append(out, best.To4())
	}
	out = append(out, normal...)
	return append(out, bridge...)
}

// systemLANAddrs reads the machine's interfaces and returns lanAddrs for them.
func systemLANAddrs() ([]net.IP, error) {
	nifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var ifs []iface
	for _, n := range nifs {
		addrs, err := n.Addrs()
		if err != nil {
			continue
		}
		ifc := iface{Name: n.Name, Flags: n.Flags}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				ifc.IPs = append(ifc.IPs, ipn.IP)
			}
		}
		ifs = append(ifs, ifc)
	}
	return lanAddrs(ifs, defaultRouteIP()), nil
}

// defaultRouteIP returns the local address the OS would use to reach the internet.
// A UDP "connect" picks a route without sending a packet.
func defaultRouteIP() net.IP {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return nil
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}
