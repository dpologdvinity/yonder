// approve.go implements --approve: every new device that opens the link must be allowed by
// the owner, at the terminal, before it sees anything. A "device" is a browser profile, told
// apart by a random id kept in its cookie, because several people can share one IP address.

package main

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
)

// maxPendingDevices is how many devices may wait for an answer at the same time. It stops
// someone who has the link from filling the owner's terminal with questions.
const maxPendingDevices = 10

// deviceState is where a device stands: waiting for the owner, allowed, or refused.
type deviceState int

const (
	// devPending means the owner has not answered yet.
	devPending deviceState = iota
	// devApproved means the owner allowed the device.
	devApproved
	// devDenied means the owner refused the device; it stays refused until yonder restarts.
	devDenied
)

// device is one browser profile that has opened the link.
type device struct {
	id        string
	addr      string // the visitor's address, for the owner's question
	userAgent string // the browser's own description of itself; never printed as it is
	state     deviceState
}

// deviceTable remembers every device seen during one run and queues the new ones for the
// owner's questions. It is safe for use from many goroutines.
type deviceTable struct {
	mu      sync.Mutex
	devices map[string]*device
	pending int
	queue   []*device     // devices not yet asked about, in arrival order
	wake    chan struct{} // tells next() that the queue has something new
}

// newDeviceTable returns an empty table.
func newDeviceTable() *deviceTable {
	return &deviceTable{
		devices: map[string]*device{},
		wake:    make(chan struct{}, 1),
	}
}

// begin records a new device in the pending state and queues it for the owner's question. It
// returns the device's id, or false when maxPendingDevices devices are already waiting.
func (t *deviceTable) begin(addr, userAgent string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending >= maxPendingDevices {
		return "", false
	}
	d := &device{id: newToken(), addr: addr, userAgent: userAgent, state: devPending}
	t.devices[d.id] = d
	t.pending++
	t.queue = append(t.queue, d)
	select {
	case t.wake <- struct{}{}:
	default: // a wake-up is already waiting
	}
	return d.id, true
}

// state returns the state of the device with this id, and whether the id is known at all.
func (t *deviceTable) state(id string) (deviceState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.devices[id]
	if !ok {
		return devDenied, false
	}
	return d.state, true
}

// waiting returns how many devices are waiting for the owner's answer.
func (t *deviceTable) waiting() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending
}

// decide records the owner's answer for a pending device. Unknown ids and devices that were
// already answered are ignored, so an answer can never be changed later.
func (t *deviceTable) decide(id string, allow bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.devices[id]
	if !ok || d.state != devPending {
		return
	}
	d.state = devDenied
	if allow {
		d.state = devApproved
	}
	t.pending--
}

// next waits for the next device that needs an answer, or returns nil when ctx ends.
func (t *deviceTable) next(ctx context.Context) *device {
	for {
		t.mu.Lock()
		if len(t.queue) > 0 {
			d := t.queue[0]
			t.queue = t.queue[1:]
			t.mu.Unlock()
			return d
		}
		t.mu.Unlock()
		select {
		case <-t.wake:
		case <-ctx.Done():
			return nil
		}
	}
}

// runPrompts asks the owner about each waiting device, one at a time and in the order they
// arrived, reading answers from in and writing questions to out. "y" or "yes" (in any case)
// allows the device; anything else, including the end of the input, refuses it. It returns
// when ctx ends.
func runPrompts(ctx context.Context, in io.Reader, out io.Writer, t *deviceTable) {
	answers := bufio.NewReader(in)
	for {
		d := t.next(ctx)
		if d == nil {
			return
		}
		say(out, "\nAllow %s at %s to open this? [y/N] ", describeUserAgent(d.userAgent), d.addr)
		line, _ := answers.ReadString('\n') // on end of input the line is empty, which means "no"
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			t.decide(d.id, true)
			say(out, "Allowed.\n")
		default:
			t.decide(d.id, false)
			say(out, "Refused.\n")
		}
	}
}

// describeUserAgent names the browser and system in a User-Agent header for the owner's
// question, such as "Safari on an iPhone". The header is chosen by the visitor, so the text
// is never repeated: only words from the fixed lists below can appear, which keeps terminal
// control characters out of the owner's screen.
func describeUserAgent(ua string) string {
	browser := ""
	for _, b := range []struct{ mark, name string }{
		{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"EdgiOS/", "Edge"}, {"OPR/", "Opera"},
		{"FxiOS/", "Firefox"}, {"Firefox/", "Firefox"}, {"CriOS/", "Chrome"}, {"Chrome/", "Chrome"},
		{"Safari/", "Safari"}, {"curl/", "curl"},
	} {
		if strings.Contains(ua, b.mark) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, s := range []struct{ mark, name string }{
		{"iPhone", "an iPhone"}, {"iPad", "an iPad"}, {"Android", "Android"}, {"Windows", "Windows"},
		{"CrOS", "ChromeOS"}, {"Macintosh", "a Mac"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, s.mark) {
			system = s.name
			break
		}
	}
	switch {
	case browser == "curl":
		return "curl"
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return "an unknown browser on " + system
	}
	return "an unknown browser"
}
