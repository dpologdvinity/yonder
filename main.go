// Command yonder shares a website running on localhost, or a file or folder, with other devices
// on the same network. It prints a link and a QR code; only people who have the link can open
// it.
//
// Usage:
//
//	yonder <port>               share the site running on localhost:<port>
//	yonder <path>               share a file or folder
//	yonder <folder> --upload    also accept files sent from other devices
//	yonder <target> --open      share without the secret token (trusted networks only)
//	yonder <target> --listen N  use N as the public port
//	yonder --version            print the version
//
// See README.md for how to use it and ARCHITECTURE.md for how it works.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v4"
)

// fileBasePort stands in for the shared port in file mode. The default listen port is always
// the shared port plus 10000, so file shares use 8000 + 10000 = 18000.
const fileBasePort = 8000

// version is the release version. goreleaser sets it at build time with -ldflags; the init
// function fills it from the module version for builds made with "go install".
var version = "dev"

// init fills version from the module's build information when the binary was built without
// goreleaser, for example by "go install github.com/dpologdvinity/yonder@latest".
func init() {
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = versionFromBuildInfo(version, bi.Main.Version)
	}
}

// versionFromBuildInfo returns the version to report. A version that goreleaser already set
// wins; otherwise the module version is used, without its leading "v". Local builds, whose
// module version is empty or "(devel)", keep "dev".
func versionFromBuildInfo(current, module string) string {
	if current != "dev" || module == "" || module == "(devel)" {
		return current
	}
	return strings.TrimPrefix(module, "v")
}

// usage is the help text. It is printed to stdout for --help and to stderr, after the reason,
// when the arguments are wrong.
const usage = "usage: yonder <port|path> [--listen N] [--open] [--upload]\n       yonder --version\n"

// options holds the parsed command line. Exactly one of port and path is set, unless
// showVersion is true.
type options struct {
	port, listenPort  int
	path              string // set instead of port when sharing files
	open, showVersion bool
	upload            bool // accept files from other devices (folders only)
}

// main runs yonder until it receives an interrupt, terminate or hang-up signal, then exits with
// the code that run returns.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := run(os.Args[1:], os.Stdout, os.Stderr, ctx)
	stop()
	os.Exit(code)
}

// parseArgs accepts flags before or after the port.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("yonder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&o.listenPort, "listen", 0, "")
	fs.BoolVar(&o.open, "open", false, "")
	fs.BoolVar(&o.upload, "upload", false, "")
	fs.BoolVar(&o.showVersion, "version", false, "")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.showVersion {
		return options{showVersion: true}, nil
	}
	if fs.NArg() == 0 {
		return options{}, errors.New("missing port")
	}
	if fs.Arg(0) == "" {
		return options{}, errors.New("empty argument")
	}
	if port, err := strconv.Atoi(fs.Arg(0)); err != nil {
		o.path = fs.Arg(0)
	} else if port < 1 || port > 65535 {
		return options{}, fmt.Errorf("invalid port %q", fs.Arg(0))
	} else {
		o.port = port
	}
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return options{}, err
	}
	if o.showVersion {
		return options{showVersion: true}, nil
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if o.upload && o.path == "" {
		return options{}, errors.New("--upload needs a folder")
	}
	if o.listenPort < 0 || o.listenPort > 65535 {
		return options{}, fmt.Errorf("invalid --listen port %d", o.listenPort)
	}
	return o, nil
}

// run is yonder's whole life cycle, separated from main so tests can call it. It parses args,
// finds what to share, starts the web server on all interfaces, prints the link and QR code to
// stdout, and serves until ctx is cancelled. It returns the process exit code: 0 after a clean
// stop, 1 for any error, which is reported on stderr.
func run(args []string, stdout, stderr io.Writer, ctx context.Context) int {
	opts, err := parseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		say(stdout, "%s", usage)
		return 0
	}
	if err != nil {
		say(stderr, "yonder: %v\n%s", err, usage)
		return 1
	}
	if opts.showVersion {
		say(stdout, "yonder %s\n", version)
		return 0
	}
	fail := func(err error) int {
		say(stderr, "yonder: %v\n", err)
		return 1
	}

	var handler http.Handler
	var what string
	port := opts.port
	if opts.path != "" {
		h, abs, closeRoot, err := fileHandler(opts.path, opts.upload)
		if err != nil {
			return fail(err)
		}
		defer func() { _ = closeRoot() }() // best effort: the process is about to exit
		handler, what, port = h, abs, fileBasePort
	} else {
		target, err := findTarget(port)
		if err != nil {
			return fail(err)
		}
		handler, what = newProxy(target, port), fmt.Sprintf("localhost:%d", port)
	}
	addrs, err := systemLANAddrs()
	if err != nil {
		return fail(err)
	}
	if len(addrs) == 0 {
		return fail(noAddrError(defaultRouteIP()))
	}
	ln, err := listen(port, opts.listenPort)
	if err != nil {
		return fail(err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port

	suffix, warning := "", "Anyone on this network can open this URL (--open)."
	if !opts.open {
		token := newToken()
		handler = withAuth(handler, token, actual)
		suffix, warning = "/?yonder="+token, "Only people with this link or QR code can open it."
	}

	url := fmt.Sprintf("http://%s:%d%s", addrs[0], actual, suffix)
	say(stdout, "yonder: sharing %s\n\n  %s\n", what, url)
	qrterminal.GenerateHalfBlock(url, qrterminal.L, stdout)
	for _, a := range addrs[1:] {
		say(stdout, "  also: http://%s:%d%s\n", a, actual, suffix)
	}
	say(stdout, "\n%s\n", warning)
	if opts.upload {
		say(stdout, "Uploads are on: files sent from other devices land in %s.\n", what)
	}
	if hint := detectWSLHint(); hint != "" {
		say(stdout, "\n%s", hint)
	}
	say(stdout, "\n")

	// No write timeout: SSE and WebSockets are long-lived. The header timeout stops slowloris.
	srv := &http.Server{Handler: withLog(handler, stdout, time.Now), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ConnState: newConnLogger(stdout, time.Now)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		// Close, not Shutdown: Shutdown waits on hijacked WebSocket connections.
		_ = srv.Close() // best effort: the process is about to exit
		// Let half-finished uploads remove their temp files before the shared folder closes.
		waitUploads(3 * time.Second)
		return 0
	case err := <-served:
		return fail(err)
	}
}

// fileHandler opens path for sharing: a folder is served whole, a file alone.
// The caller must call closeRoot when done.
func fileHandler(p string, upload bool) (h http.Handler, abs string, closeRoot func() error, err error) {
	abs, err = filepath.Abs(p)
	if err != nil {
		return nil, "", nil, err
	}
	// Resolve links first: os.Root will not follow one out of the shared folder.
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return nil, "", nil, fmt.Errorf("no such file or folder: %s", p)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, "", nil, fmt.Errorf("no such file or folder: %s", p)
	}
	// The server applies visibleName to every request, so a file it would refuse
	// must be refused here too, not shared as a link that always gives 404.
	if !info.IsDir() && !visibleName(info.Name(), runtime.GOOS == "windows") {
		return nil, "", nil, fmt.Errorf("this file name cannot be shared: %s", p)
	}
	if upload && !info.IsDir() {
		return nil, "", nil, errors.New("--upload needs a folder")
	}
	dir, only := abs, ""
	if !info.IsDir() {
		dir, only = filepath.Dir(abs), filepath.Base(abs)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", nil, err
	}
	return newFileServer(root, only, upload), abs, root.Close, nil
}

// noAddrError explains why no shareable address was found. best is the address of
// the default route, or nil when the machine has none.
func noAddrError(best net.IP) error {
	if best != nil && !best.IsPrivate() && !best.IsLoopback() {
		return fmt.Errorf("this network gives your computer a public address (%s); yonder only shares on private networks", best)
	}
	return errors.New("no network connection found")
}

// say writes formatted output to w. A failed write (a closed terminal, an HTTP client
// that went away) cannot be handled usefully, so the error is dropped here, in one place.
func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
