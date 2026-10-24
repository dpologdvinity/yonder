// main_test.go tests the command line: argument parsing, usage and error messages, exit codes,
// and how a path becomes something to share.

package main

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseArgs checks that valid arguments, in any order, give the right options and that
// invalid ones give an error.
func TestParseArgs(t *testing.T) {
	ok := []struct {
		args []string
		want options
	}{
		{[]string{"3000"}, options{port: 3000}},
		{[]string{"3000", "--listen", "4000"}, options{port: 3000, listenPort: 4000}},
		{[]string{"--listen", "4000", "3000"}, options{port: 3000, listenPort: 4000}},
		{[]string{"3000", "--open"}, options{port: 3000, open: true}},
		{[]string{"--open", "3000"}, options{port: 3000, open: true}},
		{[]string{"./docs"}, options{path: "./docs"}},
		{[]string{"notes.pdf", "--open"}, options{path: "notes.pdf", open: true}},
		{[]string{"abc"}, options{path: "abc"}},
		{[]string{"./docs", "--upload"}, options{path: "./docs", upload: true}},
		{[]string{"--version"}, options{showVersion: true}},
		{[]string{"3000", "--version"}, options{showVersion: true}},
	}
	for _, tt := range ok {
		got, err := parseArgs(tt.args)
		if err != nil || got != tt.want {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v, nil", tt.args, got, err, tt.want)
		}
	}

	bad := [][]string{{}, {""}, {"3000", "--upload"}, {"0"}, {"70000"}, {"-1"}, {"3000", "4000"}, {"3000", "--listen", "x"}, {"3000", "--listen", "70000"}, {"3000", "--nope"}}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q): want error, got nil", args)
		}
	}
}

// TestRunUsage checks that a bad port prints the reason and the usage text and exits with code
// 1.
func TestRunUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"70000"}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "usage: yonder") || !strings.Contains(errOut.String(), "yonder: invalid port") {
		t.Errorf("stderr = %q, want usage", errOut.String())
	}
}

// TestRunNothingListening checks that pointing yonder at a port nobody listens on exits with
// code 1 and says so.
func TestRunNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	var out, errOut strings.Builder
	if code := run([]string{fmt.Sprint(port)}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := fmt.Sprintf("yonder: nothing is listening on localhost:%d\n", port); errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestRunHelpExitsZero checks that -h and --help print the usage text to stdout and exit with
// code 0.
func TestRunHelpExitsZero(t *testing.T) {
	for _, flagName := range []string{"-h", "--help"} {
		var out, errOut strings.Builder
		if code := run([]string{flagName}, &out, &errOut, context.Background()); code != 0 {
			t.Errorf("%s: exit code = %d, want 0", flagName, code)
		}
		if !strings.HasPrefix(out.String(), "usage: yonder <port|path> [--listen N] [--open] [--upload]") || errOut.Len() != 0 {
			t.Errorf("%s: stdout = %q, stderr = %q; want usage on stdout only", flagName, out.String(), errOut.String())
		}
	}
}

// TestRunMissingPath checks that a path that does not exist exits with code 1 and names the
// path.
func TestRunMissingPath(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"/nonexistent/x"}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "yonder: no such file or folder: /nonexistent/x\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestFileHandlerSymlinkArg checks that a symbolic link given as the thing to share is followed
// to its target.
func TestFileHandlerSymlinkArg(t *testing.T) {
	real := filepath.Join(t.TempDir(), "doc.txt")
	must(t, os.WriteFile(real, []byte("doc"), 0o644))
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not supported")
	}
	h, _, closeRoot, err := fileHandler(link, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeRoot() }()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/doc.txt", nil))
	if rec.Body.String() != "doc" {
		t.Errorf("got %d %q, want the linked file", rec.Code, rec.Body.String())
	}
}

// TestFileHandlerRejectsDotfile checks that a file whose name starts with a dot cannot be
// shared.
func TestFileHandlerRejectsDotfile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".bashrc")
	must(t, os.WriteFile(p, []byte("x"), 0o644))
	if _, _, _, err := fileHandler(p, false); err == nil || err.Error() != "this file name cannot be shared: "+p {
		t.Errorf("err = %v, want hidden-file error", err)
	}
}

// TestFileHandlerUploadNeedsFolder checks that --upload with a single file is refused.
func TestFileHandlerUploadNeedsFolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	must(t, os.WriteFile(p, []byte("x"), 0o644))
	if _, _, _, err := fileHandler(p, true); err == nil || err.Error() != "--upload needs a folder" {
		t.Errorf("err = %v, want --upload needs a folder", err)
	}
}

// TestRunUsageErrorsSayWhy checks that --upload together with a port prints the reason as well
// as the usage text.
func TestRunUsageErrorsSayWhy(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"3000", "--upload"}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "yonder: --upload needs a folder") {
		t.Errorf("stderr = %q, want the reason", errOut.String())
	}
}

// TestFileHandlerRefusesUnservableName checks that a file name the server would always refuse
// (one with a backslash) is rejected at startup, instead of producing a link that only ever
// gives 404.
func TestFileHandlerRefusesUnservableName(t *testing.T) {
	p := filepath.Join(t.TempDir(), `a\b.txt`)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Skip("file system refuses backslash names")
	}
	_, _, _, err := fileHandler(p, false)
	if err == nil || !strings.Contains(err.Error(), "this file name cannot be shared") {
		t.Errorf("err = %v, want the file-name error", err)
	}
}

// TestNoAddrError checks that a public address gets its own explanation and that anything else
// gets the general "no network connection" message.
func TestNoAddrError(t *testing.T) {
	if err := noAddrError(net.ParseIP("131.215.1.20")); !strings.Contains(err.Error(), "public address (131.215.1.20)") || !strings.Contains(err.Error(), "private") {
		t.Errorf("public best: %v", err)
	}
	for _, best := range []net.IP{nil, net.ParseIP("192.168.1.5")} {
		if err := noAddrError(best); err.Error() != "no network connection found" {
			t.Errorf("best %v: %v", best, err)
		}
	}
}
