// main_test.go tests the command line: argument parsing, usage and error messages, exit codes,
// and how a path becomes something to share.

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
		{[]string{"3000", "--for", "30m"}, options{port: 3000, forDur: 30 * time.Minute}},
		{[]string{"./docs", "--approve"}, options{path: "./docs", approve: true}},
		{[]string{"--approve", "--for", "1h", "3000"}, options{port: 3000, approve: true, forDur: time.Hour}},
		{[]string{"--for", "1d", "./docs"}, options{path: "./docs", forDur: 24 * time.Hour}},
		{[]string{"--version"}, options{showVersion: true}},
		{[]string{"3000", "--version"}, options{showVersion: true}},
	}
	for _, tt := range ok {
		got, err := parseArgs(tt.args)
		if err != nil || got != tt.want {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v, nil", tt.args, got, err, tt.want)
		}
	}

	bad := [][]string{{}, {""}, {"3000", "--upload"}, {"0"}, {"70000"}, {"-1"}, {"3000", "4000"}, {"3000", "--listen", "x"}, {"3000", "--listen", "70000"}, {"3000", "--nope"}, {"3000", "--approve", "--open"}, {"3000", "--for", "soon"}, {"3000", "--for", "0"}, {"3000", "--for"}}
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
	if code := run([]string{"70000"}, strings.NewReader(""), &out, &errOut, context.Background()); code != 1 {
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
	if code := run([]string{fmt.Sprint(port)}, strings.NewReader(""), &out, &errOut, context.Background()); code != 1 {
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
		if code := run([]string{flagName}, strings.NewReader(""), &out, &errOut, context.Background()); code != 0 {
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
	if code := run([]string{"/nonexistent/x"}, strings.NewReader(""), &out, &errOut, context.Background()); code != 1 {
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
	if code := run([]string{"3000", "--upload"}, strings.NewReader(""), &out, &errOut, context.Background()); code != 1 {
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

// TestVersionFromBuildInfo checks which version is reported: one set by goreleaser wins,
// a "go install" build uses the module version without its "v", and local builds say "dev".
func TestVersionFromBuildInfo(t *testing.T) {
	tests := []struct{ current, module, want string }{
		{"0.4.3", "v0.9.9", "0.4.3"},
		{"dev", "v0.4.3", "0.4.3"},
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
	}
	for _, tt := range tests {
		if got := versionFromBuildInfo(tt.current, tt.module); got != tt.want {
			t.Errorf("versionFromBuildInfo(%q, %q) = %q, want %q", tt.current, tt.module, got, tt.want)
		}
	}
}

// runInProcess starts yonder inside the test with args, waits until it has printed its banner,
// stops it with a cancelled context, and returns what it printed and its exit code. It
// skips the test on machines with no shareable network address.
func runInProcess(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	var out syncBuf
	var errOut syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(args, strings.NewReader(""), &out, &errOut, ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "can open") { // both warning lines contain it, and it is the banner's last line
		select {
		case c := <-done:
			if strings.Contains(errOut.String(), "no network connection") || strings.Contains(errOut.String(), "public address") {
				t.Skip("no private network address on this machine")
			}
			t.Fatalf("yonder exited early with %d: %s", c, errOut.String())
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no banner within 10s; stdout %q stderr %q", out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("yonder did not stop after its context was cancelled")
	}
	return out.String(), code
}

// TestRunSharesAPortInProcess checks the whole start-up path of port mode inside the test:
// the banner names the shared port and carries a tokened link, and cancelling stops yonder
// with exit code 0.
func TestRunSharesAPortInProcess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	out, code := runInProcess(t, fmt.Sprint(port), "--listen", fmt.Sprint(freePort(t)))
	if code != 0 || !strings.Contains(out, fmt.Sprintf("yonder: sharing localhost:%d", port)) || !strings.Contains(out, "/?yonder=") {
		t.Errorf("code %d, output %q; want exit 0 and a banner with a tokened link", code, out)
	}
}

// TestRunSharesAFolderWithUploadInProcess checks the start-up path of folder mode: the banner
// names the folder, says uploads are on, and cancelling stops yonder with exit code 0.
func TestRunSharesAFolderWithUploadInProcess(t *testing.T) {
	dir := t.TempDir()
	out, code := runInProcess(t, dir, "--upload", "--listen", fmt.Sprint(freePort(t)))
	if code != 0 || !strings.Contains(out, "Uploads are on") || !strings.Contains(out, filepath.Base(dir)) {
		t.Errorf("code %d, output %q; want exit 0, the folder name and the uploads line", code, out)
	}
}

// TestRunOpenModeInProcess checks that --open prints a link without a token and says that
// anyone on the network can open it.
func TestRunOpenModeInProcess(t *testing.T) {
	out, code := runInProcess(t, t.TempDir(), "--open", "--listen", fmt.Sprint(freePort(t)))
	if code != 0 || strings.Contains(out, "yonder=") || !strings.Contains(out, "Anyone on this network can open this URL (--open)") {
		t.Errorf("code %d, output %q; want exit 0, no token and the --open warning", code, out)
	}
}

// TestParseDuration checks the --for values: Go durations, whole days with "d", the limits
// (above zero, at most 7 days) and the error for anything else.
func TestParseDuration(t *testing.T) {
	good := map[string]time.Duration{
		"90s": 90 * time.Second, "30m": 30 * time.Minute, "2h": 2 * time.Hour,
		"1d": 24 * time.Hour, "1d12h": 36 * time.Hour, "7d": 7 * 24 * time.Hour,
		"168h": 7 * 24 * time.Hour, "500ms": 500 * time.Millisecond,
	}
	for in, want := range good {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0s", "-5m", "8d", "169h", "soon", "1x", "d", "1d-2h", "1.5d"} {
		if _, err := parseDuration(in); err == nil || !strings.Contains(err.Error(), "invalid --for") {
			t.Errorf("parseDuration(%q): err = %v, want an 'invalid --for' error", in, err)
		}
	}
}

// TestRunStopsWhenTheTimeIsUp checks that --for ends the run by itself: yonder says the time
// is up, exits with code 0, and the banner told the owner when the link stops working.
func TestRunStopsWhenTheTimeIsUp(t *testing.T) {
	var out, errOut syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run([]string{t.TempDir(), "--for", "1500ms", "--listen", fmt.Sprint(freePort(t))}, strings.NewReader(""), &out, &errOut, context.Background())
	}()
	select {
	case code := <-done:
		if strings.Contains(errOut.String(), "no network connection") || strings.Contains(errOut.String(), "public address") {
			t.Skip("no private network address on this machine")
		}
		if code != 0 || !strings.Contains(out.String(), "The time is up, so yonder is stopping.") || !strings.Contains(out.String(), "This link works for 1.5s") {
			t.Errorf("code %d, stdout %q, stderr %q; want exit 0, the banner line and the stop message", code, out.String(), errOut.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("yonder did not stop by itself after --for elapsed")
	}
}

// TestRunApproveNeedsATerminal checks that --approve refuses to start when standard input is a
// file and not a terminal, because there would be nobody to answer the questions.
func TestRunApproveNeedsATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out, errOut strings.Builder
	code := run([]string{t.TempDir(), "--approve"}, f, &out, &errOut, context.Background())
	if code != 1 || !strings.Contains(errOut.String(), "--approve needs a terminal to ask you on") {
		t.Errorf("code %d, stderr %q; want exit 1 and the terminal message", code, errOut.String())
	}
}

// TestRunApproveEndToEnd checks the real thing inside the test: with --approve, a browser that
// opens the link waits, the owner types "y" on stdin, and the browser's next request gets the
// shared file. A second browser that is refused never does.
func TestRunApproveEndToEnd(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644))
	stdinR, stdinW := io.Pipe()
	defer func() { _ = stdinW.Close() }()
	var out, errOut syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run([]string{dir, "--approve", "--listen", fmt.Sprint(freePort(t))}, stdinR, &out, &errOut, ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("yonder did not stop")
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	var link string
	for link == "" {
		if m := regexp.MustCompile(`http://[0-9.]+:\d+/\?yonder=\S+`).FindString(out.String()); m != "" {
			link = m
		}
		select {
		case <-done:
			if strings.Contains(errOut.String(), "no network connection") || strings.Contains(errOut.String(), "public address") {
				t.Skip("no private network address on this machine")
			}
			t.Fatalf("yonder exited early: %s", errOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no link within 10s; stdout %q stderr %q", out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	base := link[:strings.Index(link, "/?")]

	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar}
	}
	fetch := func(c *http.Client, url string) (int, string) {
		code, body := get(t, c, url)
		return code, body
	}
	waitForPrompt := func(n int) {
		for strings.Count(out.String(), "to open this? [y/N]") < n {
			if time.Now().After(deadline) {
				t.Fatalf("prompt %d never appeared; stdout %q", n, out.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	alice, bob := browser(), browser()
	if code, body := fetch(alice, link); code != http.StatusAccepted || !strings.Contains(body, "Waiting for approval") {
		t.Fatalf("alice before approval: %d %q; want the waiting page", code, body)
	}
	waitForPrompt(1)
	if _, err := io.WriteString(stdinW, "y\n"); err != nil {
		t.Fatal(err)
	}
	for {
		code, body := fetch(alice, base+"/hello.txt")
		if code == http.StatusOK && body == "hi" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alice never got the file; last %d %q", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if code, _ := fetch(bob, link); code != http.StatusAccepted {
		t.Fatalf("bob before approval: %d, want 202", code)
	}
	waitForPrompt(2)
	if _, err := io.WriteString(stdinW, "n\n"); err != nil {
		t.Fatal(err)
	}
	for {
		code, _ := fetch(bob, base+"/hello.txt")
		if code == http.StatusForbidden {
			break
		}
		if code == http.StatusOK || time.Now().After(deadline) {
			t.Fatalf("bob after refusal: %d, want 403", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
