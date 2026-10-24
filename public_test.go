// public_test.go tests --public (sharing over the internet through a Cloudflare tunnel): which
// request headers are trusted and on which listener, the secure cookie, grouping of visitor
// addresses, starting and watching the cloudflared program, and the whole path through a
// stand-in cloudflared.

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStripProxyHeaders checks that every header that could carry a forged visitor address or
// scheme is removed, in any capitalisation, and that ordinary headers stay.
func TestStripProxyHeaders(t *testing.T) {
	h := http.Header{}
	for _, name := range []string{"CF-Connecting-IP", "cf-ipcountry", "CF-Ray", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded", "True-Client-IP", "X-Real-IP"} {
		h[http.CanonicalHeaderKey(name)] = []string{"x"}
	}
	h.Set("Accept", "text/html")
	stripProxyHeaders(h)
	if len(h) != 1 || h.Get("Accept") != "text/html" {
		t.Errorf("headers left: %v, want only Accept", h)
	}
}

// edgeProbe is a handler that records what it sees after the edge middleware ran.
type edgeProbe struct {
	remoteAddr, country string
	secure              bool
	header              http.Header
}

// ServeHTTP records the request's address, country, security flag and headers.
func (p *edgeProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.remoteAddr, p.country, p.secure, p.header = r.RemoteAddr, requestCountry(r), requestIsSecure(r), r.Header.Clone()
}

// TestTunnelEdgeTrustsCloudflareHeaders checks the tunnel listener: the visitor's address and
// country come from the Cloudflare headers, the request counts as secure, and no proxy header
// reaches the next handler.
func TestTunnelEdgeTrustsCloudflareHeaders(t *testing.T) {
	var p edgeProbe
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("CF-IPCountry", "US")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	withTunnelEdge(&p).ServeHTTP(httptest.NewRecorder(), req)
	if p.remoteAddr != "203.0.113.9:0" || p.country != "US" || !p.secure {
		t.Errorf("addr %q country %q secure %v; want 203.0.113.9:0, US, true", p.remoteAddr, p.country, p.secure)
	}
	for _, name := range []string{"CF-Connecting-IP", "CF-IPCountry", "X-Forwarded-For"} {
		if p.header.Get(name) != "" {
			t.Errorf("header %s reached the next handler", name)
		}
	}
}

// TestTunnelEdgeIgnoresBadValues checks that a missing or malformed address header leaves the
// connection's own address, and that a country that is not a two-letter code is dropped.
func TestTunnelEdgeIgnoresBadValues(t *testing.T) {
	for _, ip := range []string{"", "not-an-ip", "203.0.113.9, 1.2.3.4", "999.1.1.1", "\x1b[2J"} {
		var p edgeProbe
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		if ip != "" {
			req.Header.Set("CF-Connecting-IP", ip)
		}
		req.Header.Set("CF-IPCountry", "<script>")
		withTunnelEdge(&p).ServeHTTP(httptest.NewRecorder(), req)
		if p.remoteAddr != "127.0.0.1:50000" || p.country != "" {
			t.Errorf("CF-Connecting-IP %q: addr %q country %q; want the connection's address and no country", ip, p.remoteAddr, p.country)
		}
	}
}

// TestTunnelEdgeAcceptsIPv6 checks that an IPv6 visitor address is accepted and written so that
// it can be split into host and port again.
func TestTunnelEdgeAcceptsIPv6(t *testing.T) {
	var p edgeProbe
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("CF-Connecting-IP", "2001:db8::1")
	withTunnelEdge(&p).ServeHTTP(httptest.NewRecorder(), req)
	if p.remoteAddr != "[2001:db8::1]:0" {
		t.Errorf("addr %q, want [2001:db8::1]:0", p.remoteAddr)
	}
}

// TestLANEdgeIgnoresForgedCloudflareHeaders checks the most important rule: on the LAN listener
// a visitor cannot pretend to come through the tunnel. Their address stays their own, the
// request is not secure, and the forged headers are removed.
func TestLANEdgeIgnoresForgedCloudflareHeaders(t *testing.T) {
	var p edgeProbe
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.77:51000"
	req.Header.Set("CF-Connecting-IP", "8.8.8.8")
	req.Header.Set("CF-IPCountry", "US")
	req.Header.Set("X-Forwarded-Proto", "https")
	withLANEdge(&p).ServeHTTP(httptest.NewRecorder(), req)
	if p.remoteAddr != "192.168.1.77:51000" || p.secure || p.country != "" {
		t.Errorf("addr %q secure %v country %q; want the real address, not secure, no country", p.remoteAddr, p.secure, p.country)
	}
	if len(p.header.Values("Cf-Connecting-Ip"))+len(p.header.Values("X-Forwarded-Proto")) != 0 {
		t.Errorf("forged headers reached the app: %v", p.header)
	}
}

// TestAddrGroup checks how visitor addresses are grouped for the per-address limit: IPv4
// addresses stand alone, and IPv6 addresses are grouped by their /64, because one person
// usually controls a whole /64.
func TestAddrGroup(t *testing.T) {
	tests := map[string]string{
		"192.168.1.5":           "192.168.1.5",
		"203.0.113.9":           "203.0.113.9",
		"2001:db8:1:2:aaaa::1":  "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::99": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":       "2001:db8:1:3::/64",
		"::ffff:10.1.2.3":       "10.1.2.3",
		"not an address":        "not an address",
	}
	for in, want := range tests {
		if got := addrGroup(in); got != want {
			t.Errorf("addrGroup(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDeviceTableGroupsIPv6ByPrefix checks that two devices from one IPv6 /64 count as one
// visitor against the per-address limit.
func TestDeviceTableGroupsIPv6ByPrefix(t *testing.T) {
	tab := newDeviceTable()
	for i := 0; i < maxPendingPerAddress; i++ {
		if _, ok := tab.begin(fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1), "UA"); !ok {
			t.Fatalf("device %d refused", i)
		}
	}
	if _, ok := tab.begin("[2001:db8:1:2:ffff::9]:1", "UA"); ok {
		t.Error("a third device from the same /64 was accepted")
	}
	if _, ok := tab.begin("[2001:db8:1:3::1]:1", "UA"); !ok {
		t.Error("a device from another /64 was refused")
	}
}

// TestPromptShowsTheCountry checks that the approval question names the visitor's country when
// the tunnel provided one, which helps the owner spot a stranger.
func TestPromptShowsTheCountry(t *testing.T) {
	tab := newDeviceTable()
	id, _ := tab.beginFrom("203.0.113.9:0", "curl/8.4.0", "NL")
	var out syncBuf
	go runPrompts(t.Context(), strings.NewReader("n\n"), &out, tab)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := tab.state(id); st != devPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "at 203.0.113.9 (NL) to open this?") {
		t.Errorf("prompt = %q, want the address followed by (NL)", out.String())
	}
}

// TestGateSetsSecureCookieOnSecureRequests checks that the cookie is marked Secure when the
// request came in over https (through the tunnel) and only then.
func TestGateSetsSecureCookieOnSecureRequests(t *testing.T) {
	for _, secure := range []bool{true, false} {
		h := withAuth(&recorder{}, "T0K", 18000)
		if secure {
			h = withTunnelEdge(h)
		}
		req := httptest.NewRequest("GET", "/?yonder=T0K", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != secure {
			t.Errorf("secure request %v: cookies %v; want one cookie with Secure=%v", secure, cookies, secure)
		}
	}
}

// TestParseArgsPublic checks the safe defaults of --public: the token is required, sharing
// lasts an hour unless told otherwise, and every new device must be approved unless the owner
// turns that off with --no-approve.
func TestParseArgsPublic(t *testing.T) {
	ok := []struct {
		args []string
		want options
	}{
		{[]string{"3000", "--public"}, options{port: 3000, public: true, approve: true, forDur: time.Hour}},
		{[]string{"3000", "--public", "--for", "3h"}, options{port: 3000, public: true, approve: true, forDur: 3 * time.Hour}},
		{[]string{"--public", "--no-approve", "./docs"}, options{path: "./docs", public: true, noApprove: true, forDur: time.Hour}},
	}
	for _, tt := range ok {
		got, err := parseArgs(tt.args)
		if err != nil || got != tt.want {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v", tt.args, got, err, tt.want)
		}
	}
	bad := [][]string{
		{"3000", "--public", "--open"},
		{"3000", "--no-approve"},
		{"3000", "--public", "--approve", "--no-approve"},
	}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%q): want an error", args)
		}
	}
}

// TestFindCloudflaredMissing checks that when cloudflared is not installed, the error says how
// to install it on the common systems.
func TestFindCloudflaredMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := findCloudflared()
	if err == nil {
		t.Fatal("want an error when cloudflared is not installed")
	}
	for _, want := range []string{"cloudflared", "brew install cloudflared", "winget install", "pkg.cloudflare.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// TestStartTunnelFindsTheAddress checks that the public address is read from cloudflared's
// output, and that stopping the tunnel ends the program.
func TestStartTunnelFindsTheAddress(t *testing.T) {
	installFakeCloudflared(t, "")
	path, err := findCloudflared()
	if err != nil {
		t.Fatal(err)
	}
	tun, err := startTunnel(t.Context(), path, "127.0.0.1:1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^https://fake-\d+\.trycloudflare\.com$`).MatchString(tun.URL) {
		t.Errorf("URL = %q", tun.URL)
	}
	tun.stop()
	select {
	case err := <-tun.done:
		_ = err // cloudflared was killed on purpose, so an exit error is expected
	case <-time.After(5 * time.Second):
		t.Error("cloudflared kept running after stop")
	}
}

// TestStartTunnelFailsWhenNoAddressAppears checks that a cloudflared that never prints an
// address gives up after the time limit and shows the owner what it did print.
func TestStartTunnelFailsWhenNoAddressAppears(t *testing.T) {
	installFakeCloudflared(t, "nourl")
	path, _ := findCloudflared()
	_, err := startTunnel(t.Context(), path, "127.0.0.1:1", 700*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still thinking") {
		t.Errorf("err = %v, want a timeout that quotes cloudflared's last output", err)
	}
}

// TestStartTunnelReportsACrash checks that a cloudflared that exits at once is reported with its
// last output, without waiting for the time limit.
func TestStartTunnelReportsACrash(t *testing.T) {
	installFakeCloudflared(t, "crash")
	path, _ := findCloudflared()
	start := time.Now()
	_, err := startTunnel(t.Context(), path, "127.0.0.1:1", 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "something broke") || time.Since(start) > 10*time.Second {
		t.Errorf("err = %v after %v; want the crash reported quickly with its output", err, time.Since(start))
	}
}

// publicRun starts yonder in --public mode inside the test, with the stand-in cloudflared, and
// returns the banner text once it is up, plus a function that stops it and returns the exit code.
func publicRun(t *testing.T, behavior string, args ...string) (banner string, stop func() int, output func() string) {
	t.Helper()
	installFakeCloudflared(t, behavior)
	var out, errOut syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- run(args, strings.NewReader(""), &out, &errOut, ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), "can read it") {
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

// TestPublicEndToEnd checks the whole --public path with the stand-in cloudflared: the banner
// shows a public link and the warning, a request through the tunnel gets in with the token, the
// cookie is Secure, the visitor's address in the log is the one Cloudflare reported, and a
// forged Cloudflare header on the LAN link changes nothing.
func TestPublicEndToEnd(t *testing.T) {
	dir := t.TempDir()
	must(t, writeFile(dir, "hello.txt", "hi"))
	banner, stop, output := publicRun(t, "", dir, "--public", "--no-approve", "--listen", fmt.Sprint(freePort(t)))
	defer func() {
		if code := stop(); code != 0 {
			t.Errorf("exit code %d after stopping, want 0", code)
		}
	}()
	for _, want := range []string{"Public on the internet. Cloudflare carries this traffic and can read it.", "This link works for 1h"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner lacks %q:\n%s", want, banner)
		}
	}
	pub := regexp.MustCompile(`https://fake-(\d+)\.trycloudflare\.com/\?yonder=(\S+)`).FindStringSubmatch(banner)
	if pub == nil {
		t.Fatalf("no public link in the banner:\n%s", banner)
	}
	fakeURL := "http://127.0.0.1:" + pub[1]

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", fakeURL+"/?yonder="+pub[2], nil)
	req.Header.Set("X-Test-Client-IP", "203.0.113.9")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusFound || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("tunnel visit: %d cookies %v; want 302 and one Secure cookie", resp.StatusCode, cookies)
	}
	req2, _ := http.NewRequest("GET", fakeURL+"/hello.txt", nil)
	req2.AddCookie(cookies[0])
	req2.Header.Set("X-Test-Client-IP", "203.0.113.9")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != 200 || string(body) != "hi" {
		t.Errorf("file through the tunnel: %d %q, want 200 hi", resp2.StatusCode, body)
	}

	lan := regexp.MustCompile(`http://[0-9.]+:(\d+)/\?yonder=\S+`).FindStringSubmatch(banner)
	if lan == nil {
		t.Fatalf("no LAN link in the banner:\n%s", banner)
	}
	forged, _ := http.NewRequest("GET", "http://127.0.0.1:"+lan[1]+"/?yonder="+pub[2], nil)
	forged.Header.Set("CF-Connecting-IP", "8.8.8.8")
	r3, err := client.Do(forged)
	if err != nil {
		t.Fatal(err)
	}
	_ = r3.Body.Close()
	if c := r3.Cookies(); len(c) != 1 || c[0].Secure {
		t.Errorf("a forged tunnel header on the LAN link made the cookie %v; want a non-Secure cookie", c)
	}
	// The log must show the real peer for the forged request, never the forged address.
	if strings.Contains(output(), "8.8.8.8") {
		t.Errorf("the log shows the forged address 8.8.8.8:\n%s", output())
	}

	// A request through the tunnel without the token is refused.
	bare, _ := http.NewRequest("GET", fakeURL+"/hello.txt", nil)
	bare.Header.Set("X-Test-Client-IP", "203.0.113.9")
	r4, err := client.Do(bare)
	if err != nil {
		t.Fatal(err)
	}
	_ = r4.Body.Close()
	if r4.StatusCode != http.StatusForbidden {
		t.Errorf("tunnel request without the token: %d, want 403", r4.StatusCode)
	}
}

// TestPublicNeedsCloudflared checks that --public without cloudflared installed exits with
// code 1 and the install help, before anything is shared.
func TestPublicNeedsCloudflared(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out, errOut strings.Builder
	code := run([]string{t.TempDir(), "--public", "--no-approve"}, strings.NewReader(""), &out, &errOut, context.Background())
	if code != 1 || !strings.Contains(errOut.String(), "brew install cloudflared") {
		t.Errorf("code %d, stderr %q; want exit 1 and the install help", code, errOut.String())
	}
}

// TestPublicStopsWhenTheTunnelDies checks that if cloudflared exits while sharing, yonder stops
// with an error instead of looking as if it still shared something.
func TestPublicStopsWhenTheTunnelDies(t *testing.T) {
	installFakeCloudflared(t, "dieslate")
	var out, errOut syncBuf
	done := make(chan int, 1)
	go func() {
		done <- run([]string{t.TempDir(), "--public", "--no-approve", "--listen", fmt.Sprint(freePort(t))}, strings.NewReader(""), &out, &errOut, context.Background())
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), "can read it") {
		select {
		case <-done:
			if strings.Contains(errOut.String(), "no network connection") || strings.Contains(errOut.String(), "public address") {
				t.Skip("no private network address on this machine")
			}
			t.Fatalf("exited early: %s", errOut.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("no banner")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(errOut.String(), "the tunnel stopped") {
			t.Errorf("code %d, stderr %q; want exit 1 and 'the tunnel stopped'", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("yonder kept running after the tunnel died")
	}
}

// TestStartTunnelWaitsUntilTheTunnelIsRegistered checks that the address is not handed out as
// soon as it is printed. The real cloudflared prints it before the tunnel is connected, and a
// visitor who tries it in that moment gets an error that their device may remember for minutes.
func TestStartTunnelWaitsUntilTheTunnelIsRegistered(t *testing.T) {
	installFakeCloudflared(t, "")
	path, _ := findCloudflared()
	start := time.Now()
	tun, err := startTunnel(t.Context(), path, "127.0.0.1:1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.stop()
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("returned after %v, before the tunnel registered", elapsed)
	}
}

// TestStartTunnelFailsWhenTheTunnelNeverRegisters checks that an address without a registered
// connection is not good enough: yonder gives up with an error that says so.
func TestStartTunnelFailsWhenTheTunnelNeverRegisters(t *testing.T) {
	installFakeCloudflared(t, "noregister")
	path, _ := findCloudflared()
	_, err := startTunnel(t.Context(), path, "127.0.0.1:1", 1500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not connect") {
		t.Errorf("err = %v, want 'did not connect'", err)
	}
}

// TestStopWaitsForCloudflaredToExit checks that stop does not return until cloudflared is gone.
// yonder exits right after stop, and a process that was only asked to end could be left running
// with nothing to serve.
func TestStopWaitsForCloudflaredToExit(t *testing.T) {
	installFakeCloudflared(t, "")
	path, _ := findCloudflared()
	tun, err := startTunnel(t.Context(), path, "127.0.0.1:1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tun.stop()
	select {
	case <-tun.exited:
	default:
		t.Error("stop returned while cloudflared was still running")
	}
	tun.stop() // a second call is harmless
}

// TestStartTunnelFailureLeavesNoProcess checks that a tunnel that never registers is also
// stopped and waited for before startTunnel reports the failure.
func TestStartTunnelFailureLeavesNoProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("probing a process by signal 0 is not available on Windows")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv(fakePidFileEnv, pidFile)
	installFakeCloudflared(t, "noregister")
	path, _ := findCloudflared()
	if _, err := startTunnel(t.Context(), path, "127.0.0.1:1", 600*time.Millisecond); err == nil {
		t.Fatal("expected an error")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	proc, _ := os.FindProcess(pid)
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("fake cloudflared %d is still running after the failure", pid)
	}
}

// TestStartTunnelSurvivesAVeryLongOutputLine checks that one huge line of output does not stop
// yonder from reading the lines after it. If reading stopped, cloudflared would block on a full
// pipe and the tunnel would hang.
func TestStartTunnelSurvivesAVeryLongOutputLine(t *testing.T) {
	installFakeCloudflared(t, "longline")
	path, _ := findCloudflared()
	tun, err := startTunnel(t.Context(), path, "127.0.0.1:1", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tun.stop()
}

// TestFindTunnelURL checks that the tunnel address is found in a log line, and that the address
// of Cloudflare's own API, which cloudflared prints in some error messages, is not taken for it.
func TestFindTunnelURL(t *testing.T) {
	cases := []struct{ line, want string }{
		{"INF |  https://a-b-c.trycloudflare.com  |", "https://a-b-c.trycloudflare.com"},
		{"ERR Post \"https://api.trycloudflare.com/tunnel\": EOF", ""},
		{"INF nothing here", ""},
		{"ERR https://api.trycloudflare.com then https://x-y.trycloudflare.com", "https://x-y.trycloudflare.com"},
	}
	for _, c := range cases {
		if got := findTunnelURL(c.line); got != c.want {
			t.Errorf("findTunnelURL(%q) = %q, want %q", c.line, got, c.want)
		}
	}
}

// TestPrintableRemovesTerminalControls checks that every kind of character that can steer a
// terminal or reorder text is removed from another program's output: C0 and C1 controls (U+009B
// is the 8-bit escape-sequence starter) and the bidirectional overrides.
func TestPrintableRemovesTerminalControls(t *testing.T) {
	in := "a\x1b[31mb\u009bc\u202ed\u2066e\x7ff\u0085g plain \u00e9"
	if got, want := printable(in), "a[31mbcdefg plain \u00e9"; got != want {
		t.Errorf("printable = %q, want %q", got, want)
	}
}

// TestPublicWithoutTerminalExplainsWhy checks that --public, which turns approval on by
// itself, tells the owner how to avoid the terminal rule, and does not blame an option they
// never typed.
func TestPublicWithoutTerminalExplainsWhy(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out, errOut strings.Builder
	code := run([]string{t.TempDir(), "--public"}, f, &out, &errOut, context.Background())
	if code != 1 || !strings.Contains(errOut.String(), "--no-approve") {
		t.Errorf("code %d, stderr %q; want exit 1 and a mention of --no-approve", code, errOut.String())
	}
}

// TestRefusedVisitorGroupStaysBlocked checks that refusing a device also blocks its address for
// the rest of the run, so a visitor cannot refill the waiting places with new cookies and make
// the owner answer the same question again and again. Other addresses and approved ones stay.
func TestRefusedVisitorGroupStaysBlocked(t *testing.T) {
	tab := newDeviceTable()
	id, _ := tab.begin("203.0.113.9:1", "UA")
	tab.decide(id, false)
	if _, ok := tab.begin("203.0.113.9:2", "UA"); ok {
		t.Error("a refused address was allowed to ask again")
	}
	if _, ok := tab.begin("203.0.113.10:1", "UA"); !ok {
		t.Error("another address was blocked")
	}
	good, _ := tab.begin("198.51.100.1:1", "UA")
	tab.decide(good, true)
	if _, ok := tab.begin("198.51.100.1:2", "UA"); !ok {
		t.Error("an approved address was blocked")
	}
	id6, _ := tab.begin("[2001:db8:1:2::1]:1", "UA")
	tab.decide(id6, false)
	if _, ok := tab.begin("[2001:db8:1:2::ffff]:1", "UA"); ok {
		t.Error("a refused IPv6 /64 was allowed to ask again")
	}
}

// TestGatePassesTheCountryToTheQuestion checks the path from the tunnel edge through the access
// gate to the device table: a visitor with a token and a Cloudflare country is queued with that
// country, so the owner's question can show it.
func TestGatePassesTheCountryToTheQuestion(t *testing.T) {
	tab := newDeviceTable()
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := withTunnelEdge(withAuthDevices(app, "tok", 1234, tab))
	req := httptest.NewRequest("GET", "/?yonder=tok", nil)
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	req.Header.Set("CF-IPCountry", "NL")
	h.ServeHTTP(httptest.NewRecorder(), req)
	d := tab.next(t.Context())
	if d == nil || d.country != "NL" || d.host != "203.0.113.9" {
		t.Errorf("queued device = %+v, want host 203.0.113.9 and country NL", d)
	}
}

// TestProxyOnATunnelKeepsHTTPSOriginAndRedirects checks the proxy for a visitor who came over
// https through the tunnel: a same-site https Origin is rewritten to the local one so dev servers
// that check it accept the request, and an app redirect to localhost goes back to https.
func TestProxyOnATunnelKeepsHTTPSOriginAndRedirects(t *testing.T) {
	var gotOrigin, gotHost string
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotOrigin, gotHost = r.Header.Get("Origin"), r.Host
		w.Header().Set("Location", "http://localhost:"+r.URL.Query().Get("p")+"/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer app.Close()
	port := app.Listener.Addr().(*net.TCPAddr).Port
	front := httptest.NewServer(withTunnelEdge(newProxy(app.Listener.Addr().String(), port)))
	defer front.Close()

	req, _ := http.NewRequest("POST", front.URL+"/go?p="+fmt.Sprint(port), nil)
	req.Host = "x-y.trycloudflare.com"
	req.Header.Set("Origin", "https://x-y.trycloudflare.com")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if want := fmt.Sprintf("http://localhost:%d", port); gotOrigin != want || gotHost != fmt.Sprintf("localhost:%d", port) {
		t.Errorf("app saw Origin %q Host %q, want %q", gotOrigin, gotHost, want)
	}
	if got := resp.Header.Get("Location"); got != "https://x-y.trycloudflare.com/next" {
		t.Errorf("Location = %q, want https://x-y.trycloudflare.com/next", got)
	}
}
