package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// buildBinary builds yonder once per test run.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "yonder-e2e")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "yonder")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

// startYonder runs the binary in front of an upstream that answers "hello"
// and returns the first printed URL matching urlPattern (%d is the listen port).
func startYonder(t *testing.T, urlPattern string, extra ...string) (string, *exec.Cmd, chan error) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") }))
	t.Cleanup(up.Close)
	return startYonderOn(t, fmt.Sprint(up.Listener.Addr().(*net.TCPAddr).Port), urlPattern, extra...)
}

// startYonderOn runs the binary sharing target (a port or a path).
func startYonderOn(t *testing.T, target, urlPattern string, extra ...string) (string, *exec.Cmd, chan error) {
	t.Helper()
	bin := buildBinary(t)

	free, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := free.Addr().(*net.TCPAddr).Port
	free.Close()

	args := append([]string{target, "--listen", fmt.Sprint(listenPort)}, extra...)
	cmd := exec.Command(bin, args...)
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	urlRe := regexp.MustCompile(fmt.Sprintf(urlPattern, listenPort))
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if u := urlRe.FindString(sc.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case u := <-found:
		return u, cmd, exited
	case <-exited:
		if strings.Contains(stderr.String(), "no network connection found") {
			t.Skip("no LAN address on this machine")
		}
		t.Fatalf("yonder exited early: %s", stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("no URL printed within 10s")
	}
	return "", nil, nil
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestEndToEnd(t *testing.T) {
	url, cmd, exited := startYonder(t, `http://[0-9.]+:%d/\?yonder=[A-Za-z0-9_-]{22}`)

	jar, _ := cookiejar.New(nil)
	if code, body := get(t, &http.Client{Jar: jar}, url); code != 200 || body != "hello" {
		t.Errorf("share link: got %d %q, want 200 hello", code, body)
	}
	bare := url[:strings.Index(url, "/?")] + "/"
	if code, _ := get(t, http.DefaultClient, bare); code != http.StatusForbidden {
		t.Errorf("no token: got %d, want 403", code)
	}

	if runtime.GOOS == "windows" {
		return // no os.Interrupt delivery to child processes
	}
	cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit after interrupt: %v, want code 0", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("yonder did not exit within 3s of interrupt")
	}
}

func TestEndToEndOpen(t *testing.T) {
	url, _, _ := startYonder(t, `http://[0-9.]+:%d\b`, "--open")
	if strings.Contains(url, "yonder=") {
		t.Fatalf("--open URL %q carries a token", url)
	}
	if code, body := get(t, http.DefaultClient, url); code != 200 || body != "hello" {
		t.Errorf("--open: got %d %q, want 200 hello", code, body)
	}
}

func TestEndToEndFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	url, _, _ := startYonderOn(t, dir, `http://[0-9.]+:%d/\?yonder=[A-Za-z0-9_-]{22}`)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if code, body := get(t, client, url); code != 200 || !strings.Contains(body, "hello.txt") {
		t.Fatalf("listing: got %d %q", code, body)
	}
	if code, body := get(t, client, url[:strings.Index(url, "/?")]+"/hello.txt"); code != 200 || body != "hi" {
		t.Errorf("file: got %d %q, want 200 hi", code, body)
	}
}

func TestEndToEndUpload(t *testing.T) {
	dir := t.TempDir()
	url, _, _ := startYonderOn(t, dir, `http://[0-9.]+:%d/\?yonder=[A-Za-z0-9_-]{22}`, "--upload")
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get(t, client, url) // sets the cookie

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "phone.txt")
	fw.Write([]byte("from phone"))
	mw.Close()
	resp, err := client.Post(url[:strings.Index(url, "/?")]+"/", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if b, err := os.ReadFile(filepath.Join(dir, "phone.txt")); err != nil || string(b) != "from phone" {
		t.Errorf("uploaded file = %q, %v", b, err)
	}
}

func TestEndToEndInterruptDuringUploadLeavesNoTempFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no os.Interrupt delivery to child processes")
	}
	dir := t.TempDir()
	url, cmd, exited := startYonderOn(t, dir, `http://[0-9.]+:%d/\?yonder=[A-Za-z0-9_-]{22}`, "--upload")
	jar, _ := cookiejar.New(nil)
	get(t, &http.Client{Jar: jar}, url)
	host := strings.TrimPrefix(url[:strings.Index(url, "/?")], "http://")
	token := url[strings.Index(url, "yonder=")+7:]

	// An upload that claims 1 MB and sends 10 bytes, then stalls.
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := "--B\r\nContent-Disposition: form-data; name=\"file\"; filename=\"big.bin\"\r\n\r\n0123456789"
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nCookie: yonder_%s=%s\r\nContent-Type: multipart/form-data; boundary=B\r\nContent-Length: 1000000\r\n\r\n%s",
		host, host[strings.LastIndex(host, ":")+1:], token, body)
	deadline := time.Now().Add(3 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		if len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload never created a temp file, so the test proves nothing")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cmd.Process.Signal(os.Interrupt)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("yonder did not exit")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("left in the shared folder after Ctrl-C: %s", e.Name())
	}
}
