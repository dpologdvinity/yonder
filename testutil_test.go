// testutil_test.go holds small helpers shared by the test files.

package main

import (
	"net"
	"testing"
)

// must fails the test at once if err is not nil. Use it for test setup that has to work.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// freePort returns a TCP port that nothing is using right now. Another program could take it
// before the test does, so it suits tests that can afford to retry or skip, not guarantees.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	must(t, ln.Close())
	return port
}
