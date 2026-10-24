// fake_cloudflared_test.go lets the tests run yonder's --public mode without a Cloudflare
// account. The test binary starts itself as a stand-in for the real cloudflared program: it
// prints a tunnel address on stderr the way the real one does, and forwards the requests it
// receives to the address yonder gave it, adding the headers Cloudflare would add.

package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeModeEnv names the environment variable that turns the test binary into the fake
// cloudflared, and fakeBehaviorEnv how the fake should behave.
const (
	// fakeModeEnv is the environment variable that turns the test binary into the fake.
	fakeModeEnv = "YONDER_FAKE_CLOUDFLARED"
	// fakeBehaviorEnv is the environment variable that says how the fake should behave.
	fakeBehaviorEnv = "YONDER_FAKE_BEHAVIOR"
	// fakePidFileEnv names a file the fake writes its process id to, when set.
	fakePidFileEnv = "YONDER_FAKE_PIDFILE"
)

// TestMain runs the fake cloudflared when the test binary was started as one, and the normal
// tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(fakeModeEnv) == "1" {
		runFakeCloudflared()
		return
	}
	os.Exit(m.Run())
}

// runFakeCloudflared acts like "cloudflared tunnel --url http://127.0.0.1:PORT". By default it
// listens on a loopback port, prints "https://fake-<port>.trycloudflare.com" on stderr, and
// forwards every request to the --url address, turning an X-Test-Client-IP header into the
// CF-Connecting-IP header and an X-Test-Country header into CF-IPCountry. The behavior
// variable can make it print no address ("nourl"), exit at once ("crash"), or exit two
// seconds after printing its address ("dieslate"), or print the address but never register the
// tunnel ("noregister"), or print one 200 000-character line first ("longline"). Otherwise it registers 300 ms after printing the address.
func runFakeCloudflared() {
	target := ""
	for i, a := range os.Args {
		if a == "--url" && i+1 < len(os.Args) {
			target = os.Args[i+1]
		}
	}
	if f := os.Getenv(fakePidFileEnv); f != "" {
		_ = os.WriteFile(f, []byte(fmt.Sprint(os.Getpid())), 0o600)
	}
	fmt.Fprintln(os.Stderr, "INF Requesting new quick Tunnel on trycloudflare.com...")
	if os.Getenv(fakeBehaviorEnv) == "longline" {
		fmt.Fprintln(os.Stderr, strings.Repeat("x", 200_000)) // longer than a line reader's usual limit
	}
	switch os.Getenv(fakeBehaviorEnv) {
	case "crash":
		fmt.Fprintln(os.Stderr, "ERR something broke")
		os.Exit(3)
	case "nourl":
		fmt.Fprintln(os.Stderr, "INF still thinking")
		time.Sleep(time.Hour)
		return
	}
	u, err := url.Parse(target)
	if err != nil || target == "" {
		fmt.Fprintln(os.Stderr, "ERR no --url given")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR", err)
		os.Exit(2)
	}
	if os.Getenv(fakeBehaviorEnv) == "dieslate" {
		go func() {
			time.Sleep(2 * time.Second)
			os.Exit(4)
		}()
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		pr.Out.Host = pr.In.Host // like the real tunnel, keep the public host name
		if ip := pr.In.Header.Get("X-Test-Client-IP"); ip != "" {
			pr.Out.Header.Set("CF-Connecting-IP", ip)
		}
		if c := pr.In.Header.Get("X-Test-Country"); c != "" {
			pr.Out.Header.Set("CF-IPCountry", c)
		}
	}}
	fmt.Fprintf(os.Stderr, "INF |  https://fake-%d.trycloudflare.com  |\n", ln.Addr().(*net.TCPAddr).Port)
	if os.Getenv(fakeBehaviorEnv) != "noregister" {
		// The real cloudflared prints the address first and registers with Cloudflare a moment
		// later; until then the address does not work.
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "INF Registered tunnel connection connIndex=0 location=fake01 protocol=quic")
	}
	_ = http.Serve(ln, proxy)
}

// installFakeCloudflared puts a program called cloudflared, which is this test binary acting as
// the fake, first on PATH for the rest of the test, and sets how it behaves.
func installFakeCloudflared(t *testing.T, behavior string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "cloudflared"
	if err := os.Symlink(self, filepath.Join(dir, name)); err != nil {
		t.Skip("cannot make a symbolic link here: ", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(fakeModeEnv, "1")
	t.Setenv(fakeBehaviorEnv, behavior)
}
