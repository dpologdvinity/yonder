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
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v4"
	"golang.org/x/term"
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
const usage = "usage: yonder <port|path> [--listen N] [--open] [--upload] [--for TIME] [--approve]\n       yonder --version\n"

// options holds the parsed command line. Exactly one of port and path is set, unless
// showVersion is true.
type options struct {
	port, listenPort  int
	path              string // set instead of port when sharing files
	open, showVersion bool
	upload            bool          // accept files from other devices (folders only)
	forDur            time.Duration // stop sharing after this long; 0 means no limit
	approve           bool          // the owner must allow each new device at the terminal
}

// main runs yonder until it receives an interrupt, terminate or hang-up signal, then exits with
// the code that run returns.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, ctx)
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
	fs.BoolVar(&o.approve, "approve", false, "")
	fs.BoolVar(&o.showVersion, "version", false, "")
	forText := fs.String("for", "", "")
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
	if o.approve && o.open {
		return options{}, errors.New("--approve needs the access token, so it cannot be used with --open")
	}
	if *forText != "" {
		d, err := parseDuration(*forText)
		if err != nil {
			return options{}, err
		}
		o.forDur = d
	}
	return o, nil
}

// maxFor is the longest time --for accepts. A share that should last longer is better run
// again, with a fresh link, than left open for weeks.
const maxFor = 7 * 24 * time.Hour

// dayRe matches a leading number of days in a --for value such as "1d" or "1d12h".
var dayRe = regexp.MustCompile(`^(\d+)d(.*)$`)

// parseDuration reads a --for value: a Go duration ("90s", "30m", "2h"), optionally with a
// leading whole number of days ("1d", "1d12h"). It must be above zero and at most 7 days.
func parseDuration(s string) (time.Duration, error) {
	bad := fmt.Errorf("invalid --for %q: use a time such as 30m, 2h or 1d (more than 0, at most 7d)", s)
	var days time.Duration
	rest := s
	if m := dayRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n > 7 {
			return 0, bad
		}
		days, rest = time.Duration(n)*24*time.Hour, m[2]
	}
	var d time.Duration
	if rest != "" {
		var err error
		if d, err = time.ParseDuration(rest); err != nil || d < 0 {
			return 0, bad
		}
	} else if days == 0 {
		return 0, bad
	}
	total := days + d
	if total <= 0 || total > maxFor {
		return 0, bad
	}
	return total, nil
}

// run is yonder's whole life cycle, separated from main so tests can call it. It parses args,
// finds what to share, starts the web server on all interfaces, prints the link and QR code to
// stdout, and serves until ctx is cancelled or the --for time is up. stdin is where yonder
// asks questions, such as which new devices to approve. It returns the process exit code: 0
// after a clean stop, 1 for any error, which is reported on stderr.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer, ctx context.Context) int {
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
	if err := serve(ctx, opts, stdin, stdout); err != nil {
		say(stderr, "yonder: %v\n", err)
		return 1
	}
	return 0
}

// shareTarget is what yonder shares: the handler that answers requests, a description for
// the banner, the port number the public port is derived from, and how to release it.
type shareTarget struct {
	handler http.Handler
	what    string
	port    int
	closeFn func() error
}

// chooseTarget turns the command line into something to share: a proxy to a local port, or a
// file server for a path.
func chooseTarget(opts options) (shareTarget, error) {
	if opts.path != "" {
		h, abs, closeRoot, err := fileHandler(opts.path, opts.upload)
		if err != nil {
			return shareTarget{}, err
		}
		return shareTarget{handler: h, what: abs, port: fileBasePort, closeFn: closeRoot}, nil
	}
	target, err := findTarget(opts.port)
	if err != nil {
		return shareTarget{}, err
	}
	return shareTarget{
		handler: newProxy(target, opts.port),
		what:    fmt.Sprintf("localhost:%d", opts.port),
		port:    opts.port,
		closeFn: func() error { return nil },
	}, nil
}

// serve shares what opts names and blocks until ctx is cancelled, the --for time is up, or
// the server fails. A normal stop returns nil.
func serve(ctx context.Context, opts options, stdin io.Reader, stdout io.Writer) error {
	if opts.approve && !canAsk(stdin) {
		return errors.New("--approve needs a terminal to ask you on, and yonder must be running in the foreground")
	}
	share, err := chooseTarget(opts)
	if err != nil {
		return err
	}
	defer func() { _ = share.closeFn() }() // best effort: the process is about to exit

	addrs, err := systemLANAddrs()
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return noAddrError(defaultRouteIP())
	}
	ln, err := listen(share.port, opts.listenPort)
	if err != nil {
		return err
	}
	actual := ln.Addr().(*net.TCPAddr).Port

	handler := share.handler
	suffix, warning := "", "Anyone on this network can open this URL (--open)."
	if !opts.open {
		token := newToken()
		var devices *deviceTable
		if opts.approve {
			devices = newDeviceTable()
			promptCtx, stopPrompts := context.WithCancel(ctx)
			defer stopPrompts()
			go runPrompts(promptCtx, stdin, stdout, devices)
		}
		handler = withAuthDevices(handler, token, actual, devices)
		suffix, warning = "/?yonder="+token, "Only people with this link or QR code can open it."
		if opts.approve {
			warning = "Only people with this link or QR code can open it, and you must approve each new device here."
		}
	}
	printBanner(stdout, opts, share.what, addrs, actual, suffix, warning)

	if opts.forDur > 0 {
		var cancel context.CancelCauseFunc
		ctx, cancel = expireAt(ctx, opts.forDur, time.Now)
		defer cancel(nil)
	}
	tracker := newConnTracker()
	logConn := newConnLogger(stdout, time.Now)

	// No write timeout: SSE and WebSockets are long-lived. The header timeout stops slowloris.
	srv := &http.Server{Handler: withLog(handler, stdout, time.Now), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ConnState: func(c net.Conn, s http.ConnState) {
		logConn(c, s)
		tracker.track(c, s)
	}}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errTimeUp) {
			say(stdout, "\nThe time is up, so yonder is stopping.\n")
		}
		// Close, not Shutdown: Shutdown waits on hijacked WebSocket connections.
		_ = srv.Close()    // best effort: the process is about to exit
		tracker.closeAll() // Close leaves WebSocket connections open; they must not outlive the share
		// Let half-finished uploads remove their temp files before the shared folder closes.
		waitUploads(3 * time.Second)
		return nil
	case err := <-served:
		return err
	}
}

// printBanner prints what is shared, the link (and any other links), the QR code, the access
// warning, and notes about uploads, the time limit and WSL.
func printBanner(w io.Writer, opts options, what string, addrs []net.IP, port int, suffix, warning string) {
	url := fmt.Sprintf("http://%s:%d%s", addrs[0], port, suffix)
	say(w, "yonder: sharing %s\n\n  %s\n", what, url)
	qrterminal.GenerateHalfBlock(url, qrterminal.L, w)
	for _, a := range addrs[1:] {
		say(w, "  also: http://%s:%d%s\n", a, port, suffix)
	}
	say(w, "\n%s\n", warning)
	if opts.upload {
		say(w, "Uploads are on: files sent from other devices land in %s.\n", what)
	}
	if opts.forDur > 0 {
		say(w, "This link works for %s, until %s.\n", humanDuration(opts.forDur), formatUntil(time.Now(), opts.forDur))
	}
	if hint := detectWSLHint(); hint != "" {
		say(w, "\n%s", hint)
	}
	say(w, "\n")
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

// canAsk reports whether yonder can ask the owner questions on in. Anything that is not a file
// (a pipe, or a string in a test) can be read, but a file such as /dev/null or a redirect has
// nobody behind it, so only a real terminal counts, and only while yonder is in the foreground
// of it: a background job that reads its terminal is stopped by the shell, server and all.
func canAsk(in io.Reader) bool {
	f, ok := in.(*os.File)
	return !ok || (term.IsTerminal(int(f.Fd())) && inForeground(f))
}

// errTimeUp is the cause of the context that expireAt ends when the --for time has passed.
var errTimeUp = errors.New("the time is up")

// clockCheckEvery is how often expireAt compares the wall clock with the deadline. A variable
// so tests can shorten it.
var clockCheckEvery = 2 * time.Second

// expireAt returns a context that ends with the cause errTimeUp after d, or earlier if parent
// ends (then with parent's cause). It watches two clocks: a timer, which counts running time,
// and the wall clock, checked every clockCheckEvery. On Linux the timer stops while the computer
// sleeps, so a link meant to last an hour would otherwise live on after the laptop is opened
// again; the wall clock check ends it at the time the owner was told.
func expireAt(parent context.Context, d time.Duration, now func() time.Time) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	deadline := now().Round(0).Add(d) // Round(0) drops the timer reading, leaving wall-clock time
	timer := time.AfterFunc(d, func() { cancel(errTimeUp) })
	every := clockCheckEvery // read here, in the caller, so the goroutine never touches the variable
	go func() {
		defer timer.Stop()
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !now().Round(0).Before(deadline) {
					cancel(errTimeUp)
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// humanDuration writes d for people: "30m", "2h", "1m30s", "1d12h", "7d". Whole days are
// shown as "d", which Go's own format lacks.
func humanDuration(d time.Duration) string {
	var b strings.Builder
	if days := d / (24 * time.Hour); days > 0 {
		fmt.Fprintf(&b, "%dd", days)
		d -= days * 24 * time.Hour
	}
	if h := d / time.Hour; h > 0 {
		fmt.Fprintf(&b, "%dh", h)
		d -= h * time.Hour
	}
	if m := d / time.Minute; m > 0 {
		fmt.Fprintf(&b, "%dm", m)
		d -= m * time.Minute
	}
	if d > 0 {
		b.WriteString(d.String())
	}
	return b.String()
}

// formatUntil writes the moment start+d for the banner: just the clock time for a limit under a
// day, and the weekday and date as well for longer ones, which would be ambiguous.
func formatUntil(start time.Time, d time.Duration) string {
	end := start.Add(d)
	if d < 24*time.Hour {
		return end.Format("15:04")
	}
	return end.Format("Mon 2 Jan 15:04")
}
