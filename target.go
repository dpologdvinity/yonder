// target.go checks which loopback address the dev server is really listening on.

package main

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// findTarget returns the loopback address that accepts TCP on port.
// IPv4 is tried first; Node 17+ servers often listen on ::1 only.
func findTarget(port int) (string, error) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return addr, nil
		}
	}
	return "", fmt.Errorf("nothing is listening on localhost:%d", port)
}
