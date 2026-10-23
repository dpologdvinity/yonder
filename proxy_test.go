package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const clientHost = "192.168.1.20:13000"

type frontFunc func(targetAddr string, port int) http.Handler

func plainProxy(targetAddr string, port int) http.Handler { return newProxy(targetAddr, port) }

// setup starts upstream and a proxy in front of it. It returns the front URL and the upstream port.
func setup(t *testing.T, upstream http.Handler, front frontFunc) (string, int) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	addr := up.Listener.Addr().(*net.TCPAddr)
	fr := httptest.NewServer(front(addr.String(), addr.Port))
	t.Cleanup(fr.Close)
	return fr.URL, addr.Port
}

// get sends a request to the proxy as if from a phone that used clientHost.
func do(t *testing.T, method, url string, hdr http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = clientHost
	for k, v := range hdr {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestProxyHostRewrite(t *testing.T) {
	var gotHost string
	url, port := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotHost = r.Host }), plainProxy)
	do(t, "GET", url, nil)
	if want := fmt.Sprintf("localhost:%d", port); gotHost != want {
		t.Errorf("upstream Host = %q, want %q", gotHost, want)
	}
}

func TestProxyOriginRewrite(t *testing.T) {
	var gotOrigin, gotXFH string
	url, port := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin, gotXFH = r.Header.Get("Origin"), r.Header.Get("X-Forwarded-Host")
	}), plainProxy)
	do(t, "POST", url, http.Header{"Origin": {"http://" + clientHost}})
	if want := fmt.Sprintf("http://localhost:%d", port); gotOrigin != want {
		t.Errorf("upstream Origin = %q, want %q", gotOrigin, want)
	}
	if gotXFH != "" {
		t.Errorf("upstream X-Forwarded-Host = %q, want empty", gotXFH)
	}
}

func TestProxyLocationRewrite(t *testing.T) {
	var loc string
	url, port := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusFound)
	}), plainProxy)

	tests := []struct{ in, want string }{
		{fmt.Sprintf("http://localhost:%d/a?x=1", port), "http://" + clientHost + "/a?x=1"},
		{fmt.Sprintf("http://127.0.0.1:%d/a", port), "http://" + clientHost + "/a"},
		{fmt.Sprintf("http://[::1]:%d/a", port), "http://" + clientHost + "/a"},
		{"https://github.com/login", "https://github.com/login"},
		{"/dash", "/dash"},
	}
	for _, tt := range tests {
		loc = tt.in
		if got := do(t, "GET", url, nil).Header.Get("Location"); got != tt.want {
			t.Errorf("Location %q became %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProxySetCookieDomain(t *testing.T) {
	url, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "sid=1; Domain=localhost; Path=/; HttpOnly")
		w.Header().Add("Set-Cookie", "b=2; domain=.LOCALHOST")
		w.Header().Add("Set-Cookie", "a=1; Domain=example.com")
	}), plainProxy)
	got := do(t, "GET", url, nil).Header.Values("Set-Cookie")
	want := []string{"sid=1; Path=/; HttpOnly", "b=2", "a=1; Domain=example.com"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Set-Cookie = %q, want %q", got, want)
	}
}

// echoUpgrade is an upstream that switches to a raw echo protocol, like a WebSocket.
var echoUpgrade = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "echo" {
		http.Error(w, "want upgrade", http.StatusBadRequest)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	rw.Flush()
	io.Copy(conn, rw)
})

func checkUpgrade(t *testing.T, front frontFunc) {
	t.Helper()
	url, _ := setup(t, echoUpgrade, front)
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n", clientHost)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v; want ping", buf, err)
	}
}

func checkSSE(t *testing.T, front frontFunc) {
	t.Helper()
	done := make(chan struct{})
	url, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hi\n\n")
		http.NewResponseController(w).Flush()
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}), front)
	defer close(done)

	resp := do(t, "GET", url, nil)
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if s != "data: hi\n" {
			t.Errorf("first line = %q, want %q", s, "data: hi\n")
		}
	case <-time.After(time.Second):
		t.Fatal("event not streamed within 1s")
	}
}

func TestProxyUpgrade(t *testing.T) { checkUpgrade(t, plainProxy) }
func TestProxySSE(t *testing.T)     { checkSSE(t, plainProxy) }

func TestProxyUpstreamRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()
	fr := httptest.NewServer(newProxy(addr.String(), addr.Port))
	defer fr.Close()

	resp := do(t, "GET", fr.URL, nil)
	body, _ := io.ReadAll(resp.Body)
	if want := fmt.Sprintf("yonder: localhost:%d is not responding\n", addr.Port); resp.StatusCode != 502 || string(body) != want {
		t.Fatalf("down: got %d %q, want 502 %q", resp.StatusCode, body, want)
	}

	ln, err = net.Listen("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	go srv.Serve(ln)
	defer srv.Close()

	if resp := do(t, "GET", fr.URL, nil); resp.StatusCode != 200 {
		t.Fatalf("back up: got %d, want 200", resp.StatusCode)
	}
}

func TestWithLogLine(t *testing.T) {
	var log strings.Builder
	now := func() time.Time { return time.Date(2026, 10, 23, 14, 3, 9, 0, time.Local) }
	h := withLog(http.NotFoundHandler(), &log, now)
	srv := httptest.NewServer(h)
	defer srv.Close()

	do(t, "GET", srv.URL+"/a?b=1", nil)
	if want := "14:03:09  GET /a?b=1  404  127.0.0.1\n"; log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}
}

func loggedProxy(targetAddr string, port int) http.Handler {
	return withLog(newProxy(targetAddr, port), io.Discard, time.Now)
}

func TestWithLogKeepsUpgrade(t *testing.T) { checkUpgrade(t, loggedProxy) }
func TestWithLogKeepsSSE(t *testing.T)     { checkSSE(t, loggedProxy) }

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestWithLogUpgradeStatus(t *testing.T) {
	log := &syncBuf{}
	checkUpgrade(t, func(a string, p int) http.Handler { return withLog(newProxy(a, p), log, time.Now) })
	// checkUpgrade closed the client conn; the log line lands when the proxy handler returns.
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(log.String(), "  GET /  101  ") {
		if time.Now().After(deadline) {
			t.Fatalf("log = %q, want a 101 line", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
