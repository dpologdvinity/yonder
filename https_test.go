// https_test.go tests --https from the outside: the banner, a browser that trusts yonder's root
// opening the link over TLS, the certificate pages, plain http on the https port, the proxy
// over TLS (WebSockets, Origin, redirects), and the --ca and --remove-ca commands.

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// useTempConfigDir points yonder's settings folder at a temporary folder for the test and
// returns the folder its certificate authority will use.
func useTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	return filepath.Join(dir, "yonder")
}

// trustingClient returns an HTTP client that trusts only root, keeps cookies, and, with follow
// false, does not follow redirects.
func trustingClient(root *x509.Certificate, follow bool) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(root)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{
		Jar:       jar,
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

// httpsLinkRe and caLinkRe find the share link and the certificate page link in the banner.
var (
	// httpsLinkRe finds the share link in the banner.
	httpsLinkRe = regexp.MustCompile(`https://[0-9.]+:\d+/\?yonder=[A-Za-z0-9_-]+`)
	// caLinkRe finds the link to the certificate page in the banner.
	caLinkRe = regexp.MustCompile(`https://[0-9.]+:\d+/\.yonder/ca(\?yonder=[A-Za-z0-9_-]+)?`)
)

// TestHTTPSEndToEnd shares a folder with --https and checks the journey of a phone that trusts
// yonder's root: the banner shows an https link, the certificate page link and the fingerprint;
// the server sends the whole chain; the cookie is Secure; the file and the certificate pages
// load with the token and not without; and plain http on the same port is refused.
func TestHTTPSEndToEnd(t *testing.T) {
	caFolder := useTempConfigDir(t)
	var serverErrors syncBuf
	serverLog = &serverErrors
	log.SetOutput(&serverErrors) // where the server would write without its own log
	defer func() { serverLog = os.Stderr; log.SetOutput(os.Stderr) }()
	dir := t.TempDir()
	must(t, writeFile(dir, "hello.txt", "hi"))
	banner, stop, _ := startRun(t, "SHA-256", dir, "--https", "--listen", fmt.Sprint(freePort(t)))
	defer func() {
		if code := stop(); code != 0 {
			t.Errorf("exit code %d after stopping, want 0", code)
		}
	}()
	ca, err := loadCA(caFolder)
	if err != nil {
		t.Fatal(err)
	}
	link, caLink := httpsLinkRe.FindString(banner), caLinkRe.FindString(banner)
	if link == "" || caLink == "" || !strings.Contains(caLink, "?yonder=") {
		t.Fatalf("banner lacks the https link or the tokened certificate link:\n%s", banner)
	}
	for _, want := range []string{"HTTPS is on", fingerprint(ca.root.Raw), "certificate details", "new certificate", caFolder} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner lacks %q:\n%s", want, banner)
		}
	}
	base := link[:strings.Index(link, "/?")]

	resp, err := trustingClient(ca.root, false).Get(link)
	if err != nil {
		t.Fatalf("a client that trusts only yonder's root could not connect: %v", err)
	}
	_ = resp.Body.Close()
	if n := len(resp.TLS.PeerCertificates); n != 2 {
		t.Errorf("the server sent %d certificates, want 2 (the run's and the intermediate)", n)
	}
	if resp.StatusCode != http.StatusFound || len(resp.Cookies()) != 1 || !resp.Cookies()[0].Secure {
		t.Errorf("first visit: %d, cookies %v; want 302 and one Secure cookie", resp.StatusCode, resp.Cookies())
	}

	client := trustingClient(ca.root, true)
	if code, body := get(t, client, link); code != 200 || !strings.Contains(body, "hello.txt") {
		t.Errorf("the link: %d %q, want the listing", code, body)
	}
	if code, body := get(t, client, base+"/hello.txt"); code != 200 || body != "hi" {
		t.Errorf("the file: %d %q, want 200 hi", code, body)
	}
	if code, body := get(t, trustingClient(ca.root, true), caLink); code != 200 || !strings.Contains(body, "Trust yonder on this device") {
		t.Errorf("the certificate page link: %d, want 200 and the page", code)
	}
	if code, body := get(t, client, base+caCertPath); code != 200 || body != string(ca.root.Raw) {
		t.Errorf("the certificate download: %d, %d bytes; want 200 and the root", code, len(body))
	}
	if code, _ := get(t, trustingClient(ca.root, true), base+caCertPath); code != http.StatusForbidden {
		t.Errorf("the certificate download without the token: %d, want 403", code)
	}

	plain := "http://" + strings.TrimPrefix(base, "https://") + "/"
	if code, body := get(t, &http.Client{Timeout: 5 * time.Second}, plain); code != http.StatusBadRequest || !strings.Contains(body, "HTTPS") {
		t.Errorf("plain http: %d %q, want 400 and a word about HTTPS", code, body)
	}
	if strings.Contains(serverErrors.String(), "handshake") {
		t.Errorf("a failed handshake reached the terminal: %q", serverErrors.String())
	}
}

// TestQuietHandshakesPassesOtherLines checks the https server's error log: handshake failures are
// dropped, every other line is kept.
func TestQuietHandshakesPassesOtherLines(t *testing.T) {
	var out strings.Builder
	l := log.New(quietHandshakes{&out}, "", 0)
	l.Print("http: TLS handshake error from 192.168.1.30:5000: remote error: tls: unknown certificate")
	l.Print("http: panic serving 192.168.1.30:5000: boom")
	if out.String() != "http: panic serving 192.168.1.30:5000: boom\n" {
		t.Errorf("log = %q, want only the panic line", out.String())
	}
}

// TestHTTPSProxiesOverTLS shares a local server with --https and checks the proxy over TLS: a
// WebSocket-style upgrade works both ways, the browser's own https Origin reaches the app as
// http://localhost, and the app's redirects to localhost come back as https links.
func TestHTTPSProxiesOverTLS(t *testing.T) {
	caFolder := useTempConfigDir(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := upstream.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.Handle("/ws", echoUpgrade)
	mux.HandleFunc("/origin", func(w http.ResponseWriter, r *http.Request) { say(w, "%s", r.Header.Get("Origin")) })
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://localhost:%d/there", port), http.StatusFound)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(upstream) }()
	defer func() { _ = srv.Close() }()

	banner, stop, _ := startRun(t, "SHA-256", fmt.Sprint(port), "--https", "--open", "--listen", fmt.Sprint(freePort(t)))
	defer stop()
	ca, err := loadCA(caFolder)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`https://([0-9.]+:\d+)\n`).FindStringSubmatch(banner)
	if m == nil {
		t.Fatalf("no https link in the banner:\n%s", banner)
	}
	host := m[1]
	client := trustingClient(ca.root, false)

	req, _ := http.NewRequest("POST", "https://"+host+"/origin", nil)
	req.Header.Set("Origin", "https://"+host)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if want := fmt.Sprintf("http://localhost:%d", port); string(body) != want {
		t.Errorf("the app saw Origin %q, want %q", body, want)
	}

	resp, err = client.Get("https://" + host + "/away")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if loc, want := resp.Header.Get("Location"), "https://"+host+"/there"; loc != want {
		t.Errorf("redirect to %q, want %q", loc, want)
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.root)
	conn, err := tls.Dial("tcp", host, &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n", host)
	br := bufio.NewReader(conn)
	up, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = up.Body.Close() }()
	if up.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade over TLS: %d, want 101", up.StatusCode)
	}
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Errorf("echo over TLS = %q, %v; want ping", buf, err)
	}
}

// TestConnTrackerForgetsClosedTLSConnections checks that a taken-over TLS connection, which the
// server reports as the TLS wrapper, is forgotten when it closes, like a plain one.
func TestConnTrackerForgetsClosedTLSConnections(t *testing.T) {
	tracker := newConnTracker()
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	tc := tls.Server(&trackedConn{Conn: a, tracker: tracker}, &tls.Config{})
	tracker.track(tc, http.StateHijacked)
	_ = tc.Close()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if n := len(tracker.conns); n != 0 {
		t.Errorf("%d connections still tracked after closing, want 0", n)
	}
}

// runCommand runs yonder with args to completion and returns its output, errors and exit code.
func runCommand(args ...string) (stdout, stderr string, code int) {
	var out, errOut strings.Builder
	code = run(args, strings.NewReader(""), &out, &errOut, context.Background())
	return out.String(), errOut.String(), code
}

// TestRunCAShowsAndRemovesTheAuthority checks --ca and --remove-ca: before any --https run there
// is nothing to show or remove; afterwards --ca shows the folder and the fingerprint, and
// --remove-ca deletes the files and reminds the owner to remove the certificate from devices.
func TestRunCAShowsAndRemovesTheAuthority(t *testing.T) {
	dir := useTempConfigDir(t)
	if out, _, code := runCommand("--ca"); code != 0 || !strings.Contains(out, "--https") {
		t.Errorf("--ca with none: %d %q; want 0 and a pointer to --https", code, out)
	}
	if out, _, code := runCommand("--remove-ca"); code != 0 || !strings.Contains(out, "Nothing to remove") {
		t.Errorf("--remove-ca with none: %d %q; want 0 and nothing to remove", code, out)
	}
	ca, err := loadOrCreateCA(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out, _, code := runCommand("--ca")
	if code != 0 || !strings.Contains(out, filepath.Join(dir, "ca.crt")) || !strings.Contains(out, fingerprint(ca.root.Raw)) || !strings.Contains(out, ca.root.Subject.CommonName) {
		t.Errorf("--ca: %d %q; want the file, the name and the fingerprint", code, out)
	}
	out, _, code = runCommand("--remove-ca")
	if code != 0 || !strings.Contains(out, "Removed") || !strings.Contains(out, "VPN & Device Management") || !strings.Contains(out, "User credentials") || strings.Contains(out, caPagePath) {
		t.Errorf("--remove-ca: %d %q; want 0, Removed, and where to remove it on devices (not a page that is gone)", code, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there after --remove-ca: %v", err)
	}
}

// TestRunCAReportsBrokenFiles checks that --ca explains damaged files instead of hiding them.
func TestRunCAReportsBrokenFiles(t *testing.T) {
	dir := useTempConfigDir(t)
	if _, err := loadOrCreateCA(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("nope"), 0o644))
	if _, errOut, code := runCommand("--ca"); code != 1 || !strings.Contains(errOut, "--remove-ca") {
		t.Errorf("--ca with a damaged file: %d %q; want 1 and the fix", code, errOut)
	}
}
