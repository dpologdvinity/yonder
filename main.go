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
	"strconv"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v4"
)

var version = "dev"

const usage = "usage: yonder <port> [--listen N] [--open]\n       yonder --version\n"

type options struct {
	port, listenPort  int
	open, showVersion bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	port, err := strconv.Atoi(fs.Arg(0))
	if err != nil || port < 1 || port > 65535 {
		return options{}, fmt.Errorf("invalid port %q", fs.Arg(0))
	}
	o.port = port
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return options{}, err
	}
	if o.showVersion {
		return options{showVersion: true}, nil
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
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
		fmt.Fprint(stderr, usage)
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

	port := opts.port
	target, err := findTarget(port)
	if err != nil {
		return fail(err)
	}
	addrs, err := systemLANAddrs()
	if err != nil {
		return fail(err)
	}
	if len(addrs) == 0 {
		return fail(errors.New("no network connection found"))
	}
	ln, err := listen(port, opts.listenPort)
	if err != nil {
		return fail(err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port

	var handler http.Handler = newProxy(target, port)
	suffix, warning := "", "Anyone on this network can open this URL (--open)."
	if !opts.open {
		token := newToken()
		handler = withAuth(handler, token, actual)
		suffix, warning = "/?yonder="+token, "Only people with this link or QR code can open it."
	}

	url := fmt.Sprintf("http://%s:%d%s", addrs[0], actual, suffix)
	fmt.Fprintf(stdout, "yonder: sharing localhost:%d\n\n  %s\n", port, url)
	qrterminal.GenerateHalfBlock(url, qrterminal.L, stdout)
	for _, a := range addrs[1:] {
		fmt.Fprintf(stdout, "  also: http://%s:%d%s\n", a, actual, suffix)
	}
	fmt.Fprintln(stdout, "\n"+warning)
	if hint := detectWSLHint(); hint != "" {
		fmt.Fprint(stdout, "\n"+hint)
	}
	fmt.Fprintln(stdout)

	// No write timeout: SSE and WebSockets are long-lived. The header timeout stops slowloris.
	srv := &http.Server{Handler: withLog(handler, stdout, time.Now), ReadHeaderTimeout: 10 * time.Second, ConnState: newConnLogger(stdout, time.Now)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		// Close, not Shutdown: Shutdown waits on hijacked WebSocket connections.
		srv.Close()
		return 0
	case err := <-served:
		return fail(err)
	}
}
