// proxy_test.go tests the proxy in proxy.go and the log wrappers: header and redirect
// rewriting, WebSockets, streaming, restarts, the request log, and the device-connected notice.

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

// clientHost is the address a phone would type, used as the Host header in tests.
const clientHost = "192.168.1.20:13000"

// frontFunc builds the handler placed in front of an upstream, so one test body can run with
// and without the log wrapper.
type frontFunc func(targetAddr string, port int) http.Handler

// plainProxy is the bare proxy, with nothing in front of it.
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

// reply is a response whose body has already been read and closed.
type reply struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// open sends a request to the proxy as if from a phone that used clientHost and returns
// the response with its body still open. The caller must close the body.
func open(t *testing.T, method, url string, hdr http.Header) *http.Response {
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
	return resp
}

// do is open for requests whose body is read in full: it returns the finished reply.
func do(t *testing.T, method, url string, hdr http.Header) reply {
	t.Helper()
	resp := open(t, method, url, hdr)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, body}
}

// TestProxyHostRewrite checks that the app sees Host: localhost:<port>, which dev servers that
// check the host accept.
func TestProxyHostRewrite(t *testing.T) {
	var gotHost string
	url, port := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotHost = r.Host }), plainProxy)
	do(t, "GET", url, nil)
	if want := fmt.Sprintf("localhost:%d", port); gotHost != want {
		t.Errorf("upstream Host = %q, want %q", gotHost, want)
	}
}

// TestProxyOriginRewrite checks that an Origin matching the visitor's address is rewritten to
// localhost and that no X-Forwarded-Host is added.
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

// TestProxyForeignOriginUnchanged checks that an Origin from another website is left alone, so
// the app's own cross-site checks still work.
func TestProxyForeignOriginUnchanged(t *testing.T) {
	var gotOrigin string
	url, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotOrigin = r.Header.Get("Origin") }), plainProxy)
	do(t, "POST", url, http.Header{"Origin": {"https://evil.example"}})
	if gotOrigin != "https://evil.example" {
		t.Errorf("upstream Origin = %q, want https://evil.example unchanged", gotOrigin)
	}
}

// TestProxyLocationRewrite checks that redirects to localhost addresses are pointed back at the
// visitor's address, and that redirects elsewhere and relative ones are left alone.
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
		{fmt.Sprintf("http://LOCALHOST:%d/a", port), "http://" + clientHost + "/a"},
		{fmt.Sprintf("//localhost:%d/a", port), "http://" + clientHost + "/a"},
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

// TestProxyLocationRewritePort80 checks that redirects without explicit port 80 are rewritten
// when proxying port 80, but left alone when proxying another port.
func TestProxyLocationRewritePort80(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		h := http.Header{"Location": {"http://" + host + "/login"}}
		rewriteLocation(h, 80, "http", "192.168.1.50:8080")
		if got := h.Get("Location"); got != "http://192.168.1.50:8080/login" {
			t.Errorf("port 80: got %q, want http://192.168.1.50:8080/login", got)
		}

		h3000 := http.Header{"Location": {"http://" + host + "/login"}}
		rewriteLocation(h3000, 3000, "http", "192.168.1.50:8080")
		if got := h3000.Get("Location"); got != "http://"+host+"/login" {
			t.Errorf("port 3000: got %q, want http://%s/login untouched", got, host)
		}
	}
}

// TestProxySetCookieDomain checks that Domain=localhost is removed from cookies, which browsers
// would reject on an IP address, and that other attributes stay.
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
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	_ = rw.Flush()
	_, _ = io.Copy(conn, rw)
})

// checkUpgrade opens a protocol-upgrade connection (like a WebSocket) through the handler that
// front builds, and checks that bytes flow both ways.
func checkUpgrade(t *testing.T, front frontFunc) {
	t.Helper()
	url, _ := setup(t, echoUpgrade, front)
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n", clientHost)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo = %q, %v; want ping", buf, err)
	}
}

// checkSSE checks that a server-sent event reaches the client at once instead of being held
// back.
func checkSSE(t *testing.T, front frontFunc) {
	t.Helper()
	done := make(chan struct{})
	url, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hi\n\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}), front)
	defer close(done)

	resp := open(t, "GET", url, nil)
	defer func() { _ = resp.Body.Close() }()
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

// TestProxyUpgrade checks that protocol upgrades such as WebSockets pass through the bare
// proxy.
func TestProxyUpgrade(t *testing.T) { checkUpgrade(t, plainProxy) }

// TestProxySSE checks that server-sent events stream through the bare proxy.
func TestProxySSE(t *testing.T) { checkSSE(t, plainProxy) }

// TestProxyUpstreamRestart checks that while the app is down visitors get a clear 502, and that
// once it is back the next request succeeds.
func TestProxyUpstreamRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close()
	fr := httptest.NewServer(newProxy(addr.String(), addr.Port))
	defer fr.Close()

	// Warm a keep-alive connection to the first upstream, then take it down.
	first := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	_ = first.Listener.Close()
	first.Listener, _ = net.Listen("tcp", addr.String())
	first.Start()
	if resp := do(t, "GET", fr.URL, nil); resp.StatusCode != 200 {
		t.Fatalf("warm-up: got %d, want 200", resp.StatusCode)
	}
	first.Close()

	resp := do(t, "GET", fr.URL, nil)
	if want := fmt.Sprintf("yonder: localhost:%d is not responding\n", addr.Port); resp.StatusCode != 502 || string(resp.Body) != want {
		t.Fatalf("down: got %d %q, want 502 %q", resp.StatusCode, resp.Body, want)
	}

	ln, err = net.Listen("tcp", addr.String())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	if resp := do(t, "GET", fr.URL, nil); resp.StatusCode != 200 {
		t.Fatalf("back up: got %d, want 200", resp.StatusCode)
	}
}

// TestWithLogLine checks that the request log line has the time, method, path with query,
// status and visitor address.
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

// loggedProxy is the proxy wrapped in the request log.
func loggedProxy(targetAddr string, port int) http.Handler {
	return withLog(newProxy(targetAddr, port), io.Discard, time.Now)
}

// TestWithLogKeepsUpgrade checks that the log wrapper does not break protocol upgrades.
func TestWithLogKeepsUpgrade(t *testing.T) { checkUpgrade(t, loggedProxy) }

// TestWithLogKeepsSSE checks that the log wrapper does not break event streams.
func TestWithLogKeepsSSE(t *testing.T) { checkSSE(t, loggedProxy) }

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

// Write appends p to the buffer while holding the lock.
func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }

// String returns the buffered text while holding the same lock.
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestWithLogUpgradeStatus checks that an upgraded connection is logged with status 101.
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

// TestWithLogAbortedHandlerStillLogs checks that a handler that aborts mid-stream still gets a
// log line, stamped with the time the request started.
func TestWithLogAbortedHandlerStillLogs(t *testing.T) {
	var log strings.Builder
	times := []time.Time{time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local), time.Date(2026, 1, 1, 11, 0, 0, 0, time.Local)}
	i := 0
	now := func() time.Time { t := times[i]; i++; return t }
	h := withLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }), &log, now)

	func() {
		defer func() { _ = recover() }()
		req := httptest.NewRequest("GET", "/stream", nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if want := "10:00:00  GET /stream  200  192.0.2.1\n"; log.String() != want {
		t.Errorf("log = %q, want %q (start time, written despite abort)", log.String(), want)
	}
}

// fakeConn is a net.Conn that only knows its remote address.
type fakeConn struct {
	net.Conn
	addr net.Addr
}

// RemoteAddr returns the address the test gave the fake connection.
func (f fakeConn) RemoteAddr() net.Addr { return f.addr }

// TestConnLoggerFirstConnectionPerDevice checks that the "device connected" line appears once
// per visitor address, on its first new connection only.
func TestConnLoggerFirstConnectionPerDevice(t *testing.T) {
	var log strings.Builder
	now := func() time.Time { return time.Date(2026, 10, 23, 2, 41, 53, 0, time.Local) }
	onState := newConnLogger(&log, now)
	conn := func(ip string) net.Conn {
		return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 50000}}
	}

	onState(conn("172.20.10.3"), http.StateNew)
	onState(conn("172.20.10.3"), http.StateNew)    // same device again: silent
	onState(conn("172.20.10.3"), http.StateActive) // not a new connection: silent
	onState(conn("172.20.10.4"), http.StateNew)

	want := "02:41:53  device connected  172.20.10.3\n02:41:53  device connected  172.20.10.4\n"
	if log.String() != want {
		t.Errorf("log = %q, want %q", log.String(), want)
	}
}

// TestWithLogHidesToken checks that the share token never appears in the log, even in malformed
// or encoded queries, while other parameters stay.
func TestWithLogHidesToken(t *testing.T) {
	for _, uri := range []string{
		"/a?x=1&yonder=SECRET&y=2",
		"/a?%79onder=SECRET&x=1", // percent-encoded name
		"/a?a;yonder=SECRET",     // malformed query that never authenticates
		"/a?x=1&YONDER=SECRET",
	} {
		var log strings.Builder
		h := withLog(http.NotFoundHandler(), &log, time.Now)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", uri, nil))
		if strings.Contains(log.String(), "SECRET") {
			t.Errorf("%s: token reached the log: %q", uri, log.String())
		}
	}
	var log strings.Builder
	withLog(http.NotFoundHandler(), &log, time.Now).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/a?x=1&yonder=S&y=2", nil))
	if !strings.Contains(log.String(), "GET /a?x=1&y=2  404") {
		t.Errorf("log = %q, want the other parameters kept", log.String())
	}
}

// readFromWriter records whether the response writer's ReadFrom (sendfile) was used.
type readFromWriter struct {
	httptest.ResponseRecorder
	readFromCalls int
}

// ReadFrom counts the call and then copies, standing in for a response writer that supports
// sendfile.
func (w *readFromWriter) ReadFrom(r io.Reader) (int64, error) {
	w.readFromCalls++
	return io.Copy(&w.ResponseRecorder, r)
}

// TestWithLogKeepsSendfile checks that the log wrapper still lets file downloads use the
// response writer's fast ReadFrom path.
func TestWithLogKeepsSendfile(t *testing.T) {
	var log strings.Builder
	w := &readFromWriter{ResponseRecorder: *httptest.NewRecorder()}
	h := withLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.CopyN(w, strings.NewReader("file bytes"), 10) // the path http.ServeContent takes
	}), &log, time.Now)
	h.ServeHTTP(w, httptest.NewRequest("GET", "/f", nil))
	if w.readFromCalls != 1 {
		t.Errorf("underlying ReadFrom called %d times, want 1 (downloads lose sendfile)", w.readFromCalls)
	}
	if !strings.Contains(log.String(), "GET /f  200") {
		t.Errorf("log = %q, want status 200", log.String())
	}
}

// TestStatusRecorderReadFromFallback checks that downloads still work when the real
// response writer has no fast ReadFrom path: the recorder copies the bytes itself.
func TestStatusRecorderReadFromFallback(t *testing.T) {
	var log strings.Builder
	rec := httptest.NewRecorder() // a ResponseRecorder does not implement io.ReaderFrom
	h := withLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.CopyN(w, strings.NewReader("file bytes"), 10); err != nil {
			t.Errorf("CopyN: %v", err)
		}
	}), &log, time.Now)
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/f", nil))
	if rec.Body.String() != "file bytes" || !strings.Contains(log.String(), "GET /f  200") {
		t.Errorf("body %q, log %q; want the bytes copied and status 200 logged", rec.Body.String(), log.String())
	}
}

// TestProxyLocationRewritePort80OnlyForHTTP checks that a bare "localhost" is only taken to
// mean port 80 when the redirect uses http. With https it means port 443, a different server,
// so the redirect must be left alone.
func TestProxyLocationRewritePort80OnlyForHTTP(t *testing.T) {
	h := http.Header{"Location": {"https://localhost/login"}}
	rewriteLocation(h, 80, "http", "192.168.1.50:8080")
	if got := h.Get("Location"); got != "https://localhost/login" {
		t.Errorf("got %q, want the https redirect left alone", got)
	}
}

// TestWithLogSkipsQuietRequests checks that a request the gate marks as quiet (a waiting page
// reloading every two seconds) is not logged, while ordinary requests still are.
func TestWithLogSkipsQuietRequests(t *testing.T) {
	var log strings.Builder
	h := withLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/quiet" {
			markQuiet(r)
		}
	}), &log, time.Now)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/quiet", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/loud", nil))
	if strings.Contains(log.String(), "/quiet") || !strings.Contains(log.String(), "GET /loud") {
		t.Errorf("log = %q, want only the /loud request", log.String())
	}
}

// closeCounter is a net.Conn that counts how often it was closed.
type closeCounter struct {
	net.Conn
	closed int
}

// Close counts the call instead of closing anything.
func (c *closeCounter) Close() error { c.closed++; return nil }

// TestConnTrackerClosesHijackedConnections checks that connections taken over by a handler
// (WebSockets) are closed when the server stops, because http.Server.Close leaves them open.
func TestConnTrackerClosesHijackedConnections(t *testing.T) {
	tr := newConnTracker()
	ws, plain := &closeCounter{}, &closeCounter{}
	tr.track(ws, http.StateNew)
	tr.track(ws, http.StateHijacked)
	tr.track(plain, http.StateNew)
	tr.track(plain, http.StateClosed)
	tr.closeAll()
	if ws.closed != 1 || plain.closed != 0 {
		t.Errorf("closed: websocket %d, ordinary %d; want 1 and 0", ws.closed, plain.closed)
	}
	tr.closeAll() // a second call must not close again
	if ws.closed != 1 {
		t.Errorf("a second closeAll closed the connection again (%d)", ws.closed)
	}
}

// TestConnTrackerUntracksOnClose checks that wrapped connections that close during normal
// operation are removed from the tracker, preventing a memory leak for hijacked WebSockets.
func TestConnTrackerUntracksOnClose(t *testing.T) {
	tr := newConnTracker()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	wrapped := tr.wrap(ln)
	defer func() { _ = wrapped.Close() }()

	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := wrapped.Accept()
		if err == nil {
			connCh <- c
		}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	serverConn := <-connCh
	tr.track(serverConn, http.StateHijacked)

	tr.mu.Lock()
	count := len(tr.conns)
	tr.mu.Unlock()
	if count != 1 {
		t.Fatalf("tracker count = %d, want 1", count)
	}

	_ = serverConn.Close()

	tr.mu.Lock()
	count = len(tr.conns)
	tr.mu.Unlock()
	if count != 0 {
		t.Errorf("after Close, tracker count = %d, want 0", count)
	}
}
