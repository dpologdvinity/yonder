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

const usage = "usage: yonder <port> [--listen N]\n       yonder --version\n"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(os.Args[1:], os.Stdout, os.Stderr, ctx)
	stop()
	os.Exit(code)
}

// parseArgs accepts flags before or after the port.
func parseArgs(args []string) (port, listenPort int, showVersion bool, err error) {
	fs := flag.NewFlagSet("yonder", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&listenPort, "listen", 0, "")
	fs.BoolVar(&showVersion, "version", false, "")
	if err = fs.Parse(args); err != nil {
		return 0, 0, false, err
	}
	if showVersion {
		return 0, 0, true, nil
	}
	if fs.NArg() == 0 {
		return 0, 0, false, errors.New("missing port")
	}
	port, err = strconv.Atoi(fs.Arg(0))
	if err != nil || port < 1 || port > 65535 {
		return 0, 0, false, fmt.Errorf("invalid port %q", fs.Arg(0))
	}
	if err = fs.Parse(fs.Args()[1:]); err != nil {
		return 0, 0, false, err
	}
	if fs.NArg() > 0 {
		return 0, 0, false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if listenPort < 0 || listenPort > 65535 {
		return 0, 0, false, fmt.Errorf("invalid --listen port %d", listenPort)
	}
	return port, listenPort, false, nil
}

func run(args []string, stdout, stderr io.Writer, ctx context.Context) int {
	port, listenPort, showVersion, err := parseArgs(args)
	if err != nil {
		fmt.Fprint(stderr, usage)
		return 1
	}
	if showVersion {
		fmt.Fprintf(stdout, "yonder %s\n", version)
		return 0
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "yonder: %v\n", err)
		return 1
	}

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
	ln, err := listen(port, listenPort)
	if err != nil {
		return fail(err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port

	url := fmt.Sprintf("http://%s:%d", addrs[0], actual)
	fmt.Fprintf(stdout, "yonder: sharing localhost:%d\n\n  %s\n", port, url)
	qrterminal.GenerateHalfBlock(url, qrterminal.L, stdout)
	for _, a := range addrs[1:] {
		fmt.Fprintf(stdout, "  also: http://%s:%d\n", a, actual)
	}
	fmt.Fprintln(stdout, "\nAnyone on this network can open this URL. There is no access control yet.")
	if hint := detectWSLHint(); hint != "" {
		fmt.Fprint(stdout, "\n"+hint)
	}
	fmt.Fprintln(stdout)

	srv := &http.Server{Handler: withLog(newProxy(target, port), stdout, time.Now)}
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
