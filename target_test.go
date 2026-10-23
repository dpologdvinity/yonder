package main

import (
	"fmt"
	"net"
	"testing"
)

func TestFindTargetIPv4(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := findTarget(port)
	if err != nil {
		t.Fatalf("findTarget: %v", err)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", port); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindTargetIPv6Only(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	got, err := findTarget(port)
	if err != nil {
		t.Fatalf("findTarget: %v", err)
	}
	if want := fmt.Sprintf("[::1]:%d", port); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindTargetNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	_, err = findTarget(port)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if want := fmt.Sprintf("nothing is listening on localhost:%d", port); err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
