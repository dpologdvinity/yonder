// approve.go implements --approve: every new device that opens the link must be allowed by
// the owner, at the terminal, before it sees anything. A "device" is a browser profile, told
// apart by a random id kept in its cookie, because several people can share one IP address.

package main

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// maxPendingDevices is how many devices may wait for an answer at the same time. It stops
	// someone who has the link from filling the owner's terminal with questions.
	maxPendingDevices = 10
	// maxPendingPerAddress is how many of those places one visitor address may hold, so that a
	// single visitor in a loop cannot take every place and lock the others out.
	maxPendingPerAddress = 2
	// pendingTTL is how long a waiting device may go without its page reloading before it is
	// forgotten: a closed tab or a link-preview bot must not hold a place for ever. The waiting
	// page reloads every two seconds, so a real visitor never gets near this.
	pendingTTL = 60 * time.Second
)

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
	addr      string // the visitor's address with its port, for the owner's question
	host      string // the visitor's address without the port, shown to the owner
	group     string // the visitor the address belongs to (an IPv6 /64), to limit places per visitor
	country   string // two-letter country code from the tunnel, or "" when unknown
	userAgent string // the browser's own description of itself; never printed as it is
	state     deviceState
	lastSeen  time.Time // when a waiting device last asked, to forget the ones that left
}

// deviceTable remembers every device seen during one run and queues the new ones for the
// owner's questions. It is safe for use from many goroutines.
type deviceTable struct {
	mu      sync.Mutex
	devices map[string]*device
	pending int
	queue   []*device     // devices not yet asked about, in arrival order
	wake    chan struct{} // tells next() that the queue has something new
	now     func() time.Time
}

// newDeviceTable returns an empty table that tells time with the system clock.
func newDeviceTable() *deviceTable {
	return &deviceTable{
		devices: map[string]*device{},
		wake:    make(chan struct{}, 1),
		now:     time.Now,
	}
}

// begin is beginFrom for a device whose country is not known.
func (t *deviceTable) begin(addr, userAgent string) (string, bool) {
	return t.beginFrom(addr, userAgent, "")
}

// beginFrom records a new device in the pending state and queues it for the owner's question. It
// returns the device's id, or false when too many devices are waiting, in total or from this
// visitor (an IPv4 address, or an IPv6 /64). Devices that stopped waiting are forgotten first.
func (t *deviceTable) beginFrom(addr, userAgent, country string) (string, bool) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.forgetLeavers()
	if t.pending >= maxPendingDevices {
		return "", false
	}
	group := addrGroup(host)
	fromHost := 0
	for _, d := range t.devices {
		if d.state == devPending && d.group == group {
			fromHost++
		}
	}
	if fromHost >= maxPendingPerAddress {
		return "", false
	}
	d := &device{id: newToken(), addr: addr, host: host, group: group, country: country, userAgent: userAgent, state: devPending, lastSeen: t.now()}
	t.devices[d.id] = d
	t.pending++
	t.queue = append(t.queue, d)
	select {
	case t.wake <- struct{}{}:
	default: // a wake-up is already waiting
	}
	return d.id, true
}

// forgetLeavers removes waiting devices that have not been seen for pendingTTL, from the table
// and from the queue of questions. The caller must hold t.mu.
func (t *deviceTable) forgetLeavers() {
	cutoff := t.now().Add(-pendingTTL)
	for id, d := range t.devices {
		if d.state == devPending && d.lastSeen.Before(cutoff) {
			delete(t.devices, id)
			t.pending--
		}
	}
	kept := t.queue[:0]
	for _, d := range t.queue {
		if _, alive := t.devices[d.id]; alive {
			kept = append(kept, d)
		}
	}
	t.queue = kept
}

// touch records that a waiting device just asked again, so it is not forgotten.
func (t *deviceTable) touch(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d, ok := t.devices[id]; ok && d.state == devPending {
		d.lastSeen = t.now()
	}
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
		t.forgetLeavers()
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
// allows the device; anything else, including the end of the input, refuses it. Input typed
// before a question appears is thrown away, so a stray key cannot answer a question the owner
// has not seen. It returns when ctx ends, except that it cannot interrupt a read in progress:
// the goroutine ends with the process.
func runPrompts(ctx context.Context, in io.Reader, out io.Writer, t *deviceTable) {
	for {
		d := t.next(ctx)
		if d == nil {
			return
		}
		flushInput(in)
		where := d.host
		if d.country != "" {
			where += " (" + d.country + ")"
		}
		say(out, "\nAllow %s at %s to open this? [y/N] ", describeUserAgent(d.userAgent), where)
		switch strings.ToLower(strings.TrimSpace(readLine(in))) { // end of input gives "", which means no
		case "y", "yes":
			t.decide(d.id, true)
			say(out, "Allowed.\n")
		default:
			t.decide(d.id, false)
			say(out, "Refused.\n")
		}
	}
}

// readLine reads one line from r a byte at a time, so that nothing after the line is consumed
// and read ahead. It returns the line without its newline; at the end of the input it returns
// whatever was read.
func readLine(r io.Reader) string {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			line = append(line, one[0])
		}
		if err != nil {
			break
		}
	}
	return strings.TrimSuffix(string(line), "\r")
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
