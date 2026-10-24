package main

import (
	"fmt"
	"net"
	"strconv"
	"testing"
)

func listenerPort(ln net.Listener) int { return ln.Addr().(*net.TCPAddr).Port }

// freeTargetPort returns a port p where p+10000 is a valid, currently free port.
func freeTargetPort(t *testing.T) int {
	t.Helper()
	for range 100 {
		ln, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		p := listenerPort(ln)
		_ = ln.Close()
		if p+10000 > 65535 {
			p -= 30000 // ephemeral ports are often above 55535
		}
		if p < 1 {
			continue
		}
		probe, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p+10000))
		if err == nil {
			_ = probe.Close()
			return p
		}
	}
	t.Fatal("no free port pair found")
	return 0
}

func TestListenDefault(t *testing.T) {
	p := freeTargetPort(t)
	ln, err := listen(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if got := listenerPort(ln); got != p+10000 {
		t.Errorf("got port %d, want %d", got, p+10000)
	}
}

func TestListenDefaultBusy(t *testing.T) {
	p := freeTargetPort(t)
	busy, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p+10000))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()

	ln, err := listen(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if got := listenerPort(ln); got == 0 || got == p+10000 {
		t.Errorf("got port %d, want a fallback port", got)
	}
}

func TestListenDefaultOverflow(t *testing.T) {
	ln, err := listen(60000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if got := listenerPort(ln); got == 0 || got == 70000 {
		t.Errorf("got port %d, want a fallback port", got)
	}
}

func TestListenExplicitBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	q := listenerPort(busy)

	_, err = listen(3000, q)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if want := fmt.Sprintf("port %d is in use", q); err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}
