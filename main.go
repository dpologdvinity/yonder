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

// version is set by goreleaser; `go install` builds fall back to the module version.
// fileBasePort stands in for the shared port in file mode, so the default listen
// port is fileBasePort + 10000 = 18000.
const fileBasePort = 8000

var version = "dev"

func init() {
	if bi, ok := debug.ReadBuildInfo(); ok && version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

const usage = "usage: yonder <port|path> [--listen N] [--open] [--upload]\n       yonder --version\n"

type options struct {
	port, listenPort  int
	path              string // set instead of port when sharing files
	open, showVersion bool
	upload            bool // accept files from other devices (folders only)
}

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

func run(args []string, stdout, stderr io.Writer, ctx context.Context) int {
	opts, err := parseArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "yonder: %v\n%s", err, usage)
		return 1
	}
	if opts.showVersion {
		fmt.Fprintf(stdout, "yonder %s\n", version)
		return 0
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "yonder: %v\n", err)
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
		defer closeRoot()
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
	fmt.Fprintf(stdout, "yonder: sharing %s\n\n  %s\n", what, url)
	qrterminal.GenerateHalfBlock(url, qrterminal.L, stdout)
	for _, a := range addrs[1:] {
		fmt.Fprintf(stdout, "  also: http://%s:%d%s\n", a, actual, suffix)
	}
	fmt.Fprintln(stdout, "\n"+warning)
	if opts.upload {
		fmt.Fprintf(stdout, "Uploads are on: files sent from other devices land in %s.\n", what)
	}
	if hint := detectWSLHint(); hint != "" {
		fmt.Fprint(stdout, "\n"+hint)
	}
	fmt.Fprintln(stdout)

	// No write timeout: SSE and WebSockets are long-lived. The header timeout stops slowloris.
	srv := &http.Server{Handler: withLog(handler, stdout, time.Now), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ConnState: newConnLogger(stdout, time.Now)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		// Close, not Shutdown: Shutdown waits on hijacked WebSocket connections.
		srv.Close()
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
