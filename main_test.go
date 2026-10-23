package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	ok := []struct {
		args         []string
		port, listen int
		showVersion  bool
	}{
		{[]string{"3000"}, 3000, 0, false},
		{[]string{"3000", "--listen", "4000"}, 3000, 4000, false},
		{[]string{"--listen", "4000", "3000"}, 3000, 4000, false},
		{[]string{"--version"}, 0, 0, true},
		{[]string{"3000", "--version"}, 0, 0, true},
	}
	for _, tt := range ok {
		port, listen, v, err := parseArgs(tt.args)
		if err != nil || port != tt.port || listen != tt.listen || v != tt.showVersion {
			t.Errorf("parseArgs(%q) = %d, %d, %v, %v; want %d, %d, %v, nil", tt.args, port, listen, v, err, tt.port, tt.listen, tt.showVersion)
		}
	}

	bad := [][]string{{}, {"abc"}, {"0"}, {"70000"}, {"-1"}, {"3000", "4000"}, {"3000", "--listen", "x"}, {"3000", "--listen", "70000"}, {"3000", "--nope"}}
	for _, args := range bad {
		if _, _, _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q): want error, got nil", args)
		}
	}
}

func TestRunUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"abc"}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.HasPrefix(errOut.String(), "usage: yonder") {
		t.Errorf("stderr = %q, want usage", errOut.String())
	}
}

func TestRunNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	var out, errOut strings.Builder
	if code := run([]string{fmt.Sprint(port)}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := fmt.Sprintf("yonder: nothing is listening on localhost:%d\n", port); errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

func TestRunHelpExitsZero(t *testing.T) {
	for _, flagName := range []string{"-h", "--help"} {
		var out, errOut strings.Builder
		if code := run([]string{flagName}, &out, &errOut, context.Background()); code != 0 {
			t.Errorf("%s: exit code = %d, want 0", flagName, code)
		}
		if !strings.HasPrefix(out.String(), "usage: yonder") || errOut.Len() != 0 {
			t.Errorf("%s: stdout = %q, stderr = %q; want usage on stdout only", flagName, out.String(), errOut.String())
		}
	}
}
