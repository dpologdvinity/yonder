// lan.go finds the address other devices should use to reach this computer. It prefers the main
// network and keeps container, VM and VPN addresses out of the first position, because a phone
// cannot reach those.

package main

import (
	"net"
	"strings"
)

// iface is the part of a network interface that address selection needs. It exists so tests can
// describe a machine without touching the real network.
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
		isBridge := isVirtual(ifc.Name) || ifc.Flags&net.FlagPointToPoint != 0
		for _, ip := range ifc.IPs {
			ip4 := ip.To4()
			if ip4 == nil || !ip4.IsPrivate() || seen[ip4.String()] {
				continue
			}
			seen[ip4.String()] = true
			// A tunnel or bridge never wins best: a full-tunnel VPN owns the default route but the phone cannot reach it.
			if best != nil && ip4.Equal(best) && !isBridge {
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

// isVirtual reports container bridges, VM host-only networks and VPN tunnels.
func isVirtual(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{"docker", "br-", "veth", "vboxnet", "vmnet", "bridge", "tun", "utun", "tap", "wg", "ppp"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	// Windows uses friendly names ("OpenVPN TAP-Windows6", "vEthernet (WSL)", "WireGuard Tunnel").
	for _, p := range []string{"vethernet", "virtualbox", "vmware", "wireguard", "openvpn", "tailscale", "zerotier"} {
		if strings.Contains(name, p) || strings.Contains(name, "tap-") {
			return true
		}
	}
	return false
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
