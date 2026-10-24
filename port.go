// port.go opens the listening socket that other devices connect to, and chooses its port
// number.

package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
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

// isAddrInUse reports whether err means the port is already taken: EADDRINUSE on
// Unix, or WSAEADDRINUSE (10048), which Windows reports with a different value.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048))
}
