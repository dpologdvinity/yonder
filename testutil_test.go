// testutil_test.go holds small helpers shared by the test files.

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// writeFile creates the file name in dir with the given content.
func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// startRun starts yonder inside the test with args and returns its output once it contains
// until (a line of the banner), a function that stops it and returns the exit code, and a
// function that returns everything it has printed so far. It skips the test on machines with no
// shareable network address.
func startRun(t *testing.T, until string, args ...string) (banner string, stop func() int, output func() string) {
	t.Helper()
	var out, errOut syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(args, strings.NewReader(""), &out, &errOut, ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), until) {
		select {
		case code := <-done:
			cancel()
			if strings.Contains(errOut.String(), "no network connection") || strings.Contains(errOut.String(), "public address") {
				t.Skip("no private network address on this machine")
			}
			t.Fatalf("yonder exited early with %d: %s", code, errOut.String())
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no banner; stdout %q stderr %q", out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return out.String(), func() int {
		cancel()
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("yonder did not stop")
			return -1
		}
	}, out.String
}
