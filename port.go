package main

import (
	"fmt"
	"net"
	"strconv"
)

// listen opens the proxy listener on all interfaces.
// explicit > 0 must be free. Otherwise try target+10000 so the URL survives
// restarts, falling back to a port chosen by the OS.
func listen(target, explicit int) (net.Listener, error) {
	if explicit > 0 {
		ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(explicit))
		if isAddrInUse(err) {
			return nil, fmt.Errorf("port %d is in use", explicit)
		}
		if err != nil {
			return nil, fmt.Errorf("cannot listen on port %d: %w", explicit, err)
		}
		return ln, nil
	}
	if p := target + 10000; p <= 65535 {
		if ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p)); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", "0.0.0.0:0")
}
