// target_test.go tests detecting which loopback address the dev server listens on.

package main

import (
	"fmt"
	"net"
	"testing"
)

// TestFindTargetIPv4 checks that a server on 127.0.0.1 is found there.
func TestFindTargetIPv4(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := findTarget(port)
	if err != nil {
		t.Fatalf("findTarget: %v", err)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", port); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFindTargetIPv6Only checks that a server on ::1 only, which is common with Node.js, is
// found there.
func TestFindTargetIPv6Only(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := findTarget(port)
	if err != nil {
		t.Fatalf("findTarget: %v", err)
	}
	if want := fmt.Sprintf("[::1]:%d", port); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestFindTargetNothing checks that with no server running the error names the port.
func TestFindTargetNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_, err = findTarget(port)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if want := fmt.Sprintf("nothing is listening on localhost:%d", port); err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
