// tunnel.go runs the Cloudflare quick tunnel that makes --public work. yonder does not contain
// a tunnel client: it starts the cloudflared program that the owner has installed, points it at
// a loopback address that only yonder listens on, and reads the public address cloudflared
// prints. If cloudflared stops, the share must stop, so the caller watches the done channel.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// cloudflaredHelp tells the owner how to install cloudflared on the common systems.
const cloudflaredHelp = `--public needs the cloudflared program, which is not installed. Install it with one of:
  brew install cloudflared                      (macOS and Linux with Homebrew)
  winget install --id Cloudflare.cloudflared    (Windows)
  apt and rpm packages: https://pkg.cloudflare.com
Then run yonder again`

// tunnelReadyRe finds the line cloudflared prints once the tunnel is connected to Cloudflare. The
// public address is printed earlier and does not work until then.
var tunnelReadyRe = regexp.MustCompile(`Registered tunnel connection`)

// tunnelURLRe finds addresses under trycloudflare.com in cloudflared's output.
var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// cloudflareAPIURL is Cloudflare's own service, which cloudflared names in some error messages.
// It is not a tunnel, so it is never taken for the public address.
const cloudflareAPIURL = "https://api.trycloudflare.com"

// findTunnelURL returns the address of the quick tunnel named in a line of cloudflared's output,
// or "" when the line names none.
func findTunnelURL(line string) string {
	for _, u := range tunnelURLRe.FindAllString(line, -1) {
		if u != cloudflareAPIURL {
			return u
		}
	}
	return ""
}

// maxTunnelLine is how much of one line of cloudflared's output is kept. The rest of a longer
// line is skipped, so a huge line can never stop yonder from reading what follows.
const maxTunnelLine = 64 * 1024

// stopWait is how long stop waits for cloudflared to be gone before giving up.
const stopWait = 5 * time.Second

// tunnelOutputLines is how many of the last lines cloudflared printed are kept to explain a
// failure.
const tunnelOutputLines = 12

// tunnel is a running cloudflared quick tunnel.
type tunnel struct {
	// URL is the public address, such as https://random-words.trycloudflare.com.
	URL string
	// done receives cloudflared's exit status when it stops, for whatever reason.
	done <-chan error
	// exited is closed once the cloudflared process has ended and been waited for.
	exited <-chan struct{}
	// stop ends cloudflared and waits (a few seconds at most) until it has ended, because
	// yonder exits right after and an unfinished kill could leave the process running. It is
	// safe to call more than once.
	stop func()
}

// findCloudflared returns the path of the cloudflared program, or an error that says how to
// install it.
func findCloudflared() (string, error) {
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return "", errors.New(cloudflaredHelp)
	}
	return path, nil
}

// startTunnel runs cloudflared so that it forwards its public address to localAddr (host:port,
// on this computer), and waits up to wait for the public address to appear and for the tunnel
// to be connected, because the address does not work before that. The token is never
// given to cloudflared: it only learns the loopback address. On failure the error includes the
// last lines cloudflared printed.
func startTunnel(ctx context.Context, path, localAddr string, wait time.Duration) (*tunnel, error) {
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, path, "tunnel", "--no-autoupdate", "--url", "http://"+localAddr)
	cmd.Stdout = io.Discard
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 2 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("could not start cloudflared: %w", err)
	}

	urls := make(chan string, 1)
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	exited := make(chan struct{})
	lines := &lastLines{max: tunnelOutputLines}
	// halt ends cloudflared and waits for it to be gone.
	halt := func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(stopWait):
		}
	}
	go func() {
		reader := bufio.NewReader(stderr)
		for { // keep reading for as long as cloudflared runs, or it would block
			line, err := readCapped(reader, maxTunnelLine)
			if line != "" {
				lines.add(line)
			}
			if u := findTunnelURL(line); u != "" {
				select {
				case urls <- u:
				default:
				}
			}
			if tunnelReadyRe.MatchString(line) {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
			if err != nil {
				break
			}
		}
		werr := cmd.Wait()
		close(exited)
		done <- werr
	}()

	deadline := time.After(wait)
	url, connected := "", false
	for url == "" || !connected {
		select {
		case url = <-urls:
		case <-ready:
			connected = true
		case err := <-done:
			halt()
			return nil, fmt.Errorf("cloudflared stopped before the tunnel was ready (%w)%s", err, lines.describe())
		case <-deadline:
			halt()
			if url == "" {
				return nil, fmt.Errorf("cloudflared gave no public address within %s%s", wait, lines.describe())
			}
			return nil, fmt.Errorf("the tunnel %s did not connect within %s%s", url, wait, lines.describe())
		case <-ctx.Done():
			halt()
			return nil, ctx.Err()
		}
	}
	return &tunnel{URL: url, done: done, exited: exited, stop: halt}, nil
}

// readCapped reads one line from r, without its newline, and keeps at most max bytes of it; the
// rest of a longer line is read and thrown away. It returns the error that ended the reading,
// together with any text read before it.
func readCapped(r *bufio.Reader, max int) (string, error) {
	var line []byte
	for {
		chunk, more, err := r.ReadLine()
		if room := max - len(line); room > 0 {
			line = append(line, chunk[:min(room, len(chunk))]...)
		}
		if !more || err != nil {
			return string(line), err
		}
	}
}

// lastLines keeps the last few lines of a program's output, safe for use from two goroutines.
type lastLines struct {
	mu    sync.Mutex
	max   int
	lines []string
}

// add keeps line, dropping the oldest when more than max are kept. Control characters are
// removed, because the lines may end up on the owner's terminal.
func (l *lastLines) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, printable(line))
	if len(l.lines) > l.max {
		l.lines = l.lines[1:]
	}
}

// describe returns the kept lines as an indented block that follows an error message, or ""
// when there are none.
func (l *lastLines) describe() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return "\ncloudflared said:\n  " + strings.Join(l.lines, "\n  ")
}

// printable returns s with every control character removed, so text from another program
// cannot move the cursor, change the colours of the owner's terminal, or reorder the text. That
// covers the C0 and C1 controls (U+009B starts an escape sequence on terminals that read 8-bit
// codes) and the bidirectional overrides and isolates.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r >= 0x7f && r <= 0x9f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}
