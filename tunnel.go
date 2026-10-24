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

// tunnelURLRe finds the address of a Cloudflare quick tunnel in cloudflared's output.
var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// tunnelOutputLines is how many of the last lines cloudflared printed are kept to explain a
// failure.
const tunnelOutputLines = 12

// tunnel is a running cloudflared quick tunnel.
type tunnel struct {
	// URL is the public address, such as https://random-words.trycloudflare.com.
	URL string
	// done receives cloudflared's exit status when it stops, for whatever reason.
	done <-chan error
	// stop ends cloudflared. It is safe to call more than once.
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
	lines := &lastLines{max: tunnelOutputLines}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() { // keep reading for as long as cloudflared runs, or it would block
			line := scanner.Text()
			lines.add(line)
			if u := tunnelURLRe.FindString(line); u != "" {
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
		}
		done <- cmd.Wait()
	}()

	deadline := time.After(wait)
	url, connected := "", false
	for url == "" || !connected {
		select {
		case url = <-urls:
		case <-ready:
			connected = true
		case err := <-done:
			cancel()
			return nil, fmt.Errorf("cloudflared stopped before the tunnel was ready (%w)%s", err, lines.describe())
		case <-deadline:
			cancel()
			if url == "" {
				return nil, fmt.Errorf("cloudflared gave no public address within %s%s", wait, lines.describe())
			}
			return nil, fmt.Errorf("the tunnel %s did not connect within %s%s", url, wait, lines.describe())
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		}
	}
	return &tunnel{URL: url, done: done, stop: cancel}, nil
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
// cannot move the cursor or change the colours of the owner's terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
