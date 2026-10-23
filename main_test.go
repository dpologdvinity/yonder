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
		{[]string{"--version"}, options{showVersion: true}},
		{[]string{"3000", "--version"}, options{showVersion: true}},
	}
	for _, tt := range ok {
		got, err := parseArgs(tt.args)
		if err != nil || got != tt.want {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v, nil", tt.args, got, err, tt.want)
		}
	}

	bad := [][]string{{}, {""}, {"0"}, {"70000"}, {"-1"}, {"3000", "4000"}, {"3000", "--listen", "x"}, {"3000", "--listen", "70000"}, {"3000", "--nope"}}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q): want error, got nil", args)
		}
	}
}

func TestRunUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"70000"}, &out, &errOut, context.Background()); code != 1 {
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
		if !strings.HasPrefix(out.String(), "usage: yonder <port|path> [--listen N] [--open]") || errOut.Len() != 0 {
			t.Errorf("%s: stdout = %q, stderr = %q; want usage on stdout only", flagName, out.String(), errOut.String())
		}
	}
}

func TestRunMissingPath(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"/nonexistent/x"}, &out, &errOut, context.Background()); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "yonder: no such file or folder: /nonexistent/x\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

func TestFileHandlerSymlinkArg(t *testing.T) {
	real := filepath.Join(t.TempDir(), "doc.txt")
	os.WriteFile(real, []byte("doc"), 0o644)
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not supported")
	}
	h, _, closeRoot, err := fileHandler(link)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRoot()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/doc.txt", nil))
	if rec.Body.String() != "doc" {
		t.Errorf("got %d %q, want the linked file", rec.Code, rec.Body.String())
	}
}

func TestFileHandlerRejectsDotfile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".bashrc")
	os.WriteFile(p, []byte("x"), 0o644)
	if _, _, _, err := fileHandler(p); err == nil || err.Error() != "hidden files are never shared: "+p {
		t.Errorf("err = %v, want hidden-file error", err)
	}
}
