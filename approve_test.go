// approve_test.go tests device approval (--approve): the table that remembers each device,
// the terminal questions, how the access gate treats pending, approved and denied devices,
// and that nothing about a visitor's browser can reach the terminal unfiltered.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDeviceTableLifecycle checks that a new device starts pending, that the owner's answer
// moves it to approved or denied, and that unknown ids are reported as unknown.
func TestDeviceTableLifecycle(t *testing.T) {
	tab := newDeviceTable()
	id, ok := tab.begin("192.168.1.5:50000", "UA")
	if !ok || id == "" {
		t.Fatalf("begin = %q, %v; want a new id", id, ok)
	}
	if st, known := tab.state(id); !known || st != devPending {
		t.Errorf("state = %v, %v; want pending", st, known)
	}
	tab.decide(id, true)
	if st, _ := tab.state(id); st != devApproved {
		t.Errorf("after approving, state = %v", st)
	}
	other, _ := tab.begin("192.168.1.6:50000", "UA")
	tab.decide(other, false)
	if st, _ := tab.state(other); st != devDenied {
		t.Errorf("after denying, state = %v", st)
	}
	if _, known := tab.state("nope"); known {
		t.Error("an unknown id was reported as known")
	}
	tab.decide("nope", true) // must not panic or create the id
	if _, known := tab.state("nope"); known {
		t.Error("deciding an unknown id created it")
	}
}

// TestDeviceTablePendingLimit checks that at most maxPendingDevices devices can wait at once,
// so a visitor with the link cannot flood the owner, and that answering one frees a place.
func TestDeviceTablePendingLimit(t *testing.T) {
	tab := newDeviceTable()
	var first string
	for i := 0; i < maxPendingDevices; i++ {
		id, ok := tab.begin(fmt.Sprintf("10.0.0.%d:1", i+1), "UA") // one place per address
		if !ok {
			t.Fatalf("device %d refused, limit is %d", i, maxPendingDevices)
		}
		if i == 0 {
			first = id
		}
	}
	if _, ok := tab.begin("10.0.1.1:1", "UA"); ok {
		t.Fatal("a device beyond the limit was accepted")
	}
	tab.decide(first, true)
	if _, ok := tab.begin("10.0.1.1:1", "UA"); !ok {
		t.Error("answering a device did not free a place")
	}
}

// TestDescribeUserAgent checks that the browser and system are named from a fixed list, and
// that anything else, including text meant to trick the terminal, is never echoed back.
func TestDescribeUserAgent(t *testing.T) {
	tests := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1":    "Safari on an iPhone",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 CriOS/120.0.0.0 Mobile/15E148 Safari/604.1": "Chrome on an iPhone",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile Safari/537.36":                      "Chrome on Android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15":                      "Safari on a Mac",
		"Mozilla/5.0 (X11; Linux x86_64; rv:121.0) Gecko/20100101 Firefox/121.0":                                                 "Firefox on Linux",
		"curl/8.4.0":                       "curl",
		"":                                 "an unknown browser",
		"\x1b]0;pwned\x07\x1b[2J rm -rf /": "an unknown browser",
	}
	for ua, want := range tests {
		got := describeUserAgent(ua)
		if got != want {
			t.Errorf("describeUserAgent(%q) = %q, want %q", ua, got, want)
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Errorf("describeUserAgent(%q) returned a control character", ua)
			}
		}
	}
}

// TestRunPromptsAsksAndRecordsAnswers checks the terminal conversation: each waiting device
// is described and asked about in turn, "y" and "yes" approve, anything else denies, and end
// of input denies every device that is still waiting.
func TestRunPromptsAsksAndRecordsAnswers(t *testing.T) {
	tab := newDeviceTable()
	ids := make([]string, 4)
	for i := range ids {
		ids[i], _ = tab.begin(fmt.Sprintf("192.168.1.%d:50000", 10+i), "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/604.1")
	}
	var out syncBuf
	done := make(chan struct{})
	go func() {
		runPrompts(t.Context(), strings.NewReader("y\nno\nYES\n"), &out, tab) // input ends before the 4th answer
		close(done)
	}()
	want := []deviceState{devApproved, devDenied, devApproved, devDenied}
	deadline := time.Now().Add(5 * time.Second)
	for i, id := range ids {
		for {
			if st, _ := tab.state(id); st != devPending {
				if st != want[i] {
					t.Errorf("device %d: state %v, want %v", i, st, want[i])
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("device %d was never decided; output %q", i, out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	text := out.String()
	for _, part := range []string{"Allow Safari on an iPhone at 192.168.1.10 to open this? [y/N]", "192.168.1.13"} {
		if !strings.Contains(text, part) {
			t.Errorf("prompt output lacks %q:\n%s", part, text)
		}
	}
}

// approvalGate sends one request through a gate with device approval on, and returns the
// gate's response and the stand-in app behind it.
func approvalGate(tab *deviceTable, method, target string, hdr http.Header) (*httptest.ResponseRecorder, *recorder) {
	return approvalGateFrom(tab, "192.168.1.42:51000", method, target, hdr)
}

// approvalGateFrom is approvalGate for a request that comes from the given visitor address.
func approvalGateFrom(tab *deviceTable, addr, method, target string, hdr http.Header) (*httptest.ResponseRecorder, *recorder) {
	up := &recorder{}
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = addr
	req.Header.Set("User-Agent", "curl/8.4.0")
	for k, v := range hdr {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	withAuthDevices(up, "T0K", 18000, tab).ServeHTTP(rec, req)
	return rec, up
}

// firstVisit makes the first visit with the link and returns the device cookie the gate set.
func firstVisit(t *testing.T, tab *deviceTable) string {
	t.Helper()
	rec, up := approvalGate(tab, "GET", "/?yonder=T0K", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" || up.called {
		t.Fatalf("first visit: %d Location %q called %v; want 302 to / without calling the app", rec.Code, rec.Header().Get("Location"), up.called)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yonder_18000" {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("first visit set no yonder cookie")
	return ""
}

// TestApprovalGateWaitsThenLetsApprovedDevicesIn checks the whole path: a device that has only
// visited the link waits on a self-refreshing page, and once the owner approves, its next
// request reaches the app without any yonder cookie.
func TestApprovalGateWaitsThenLetsApprovedDevicesIn(t *testing.T) {
	tab := newDeviceTable()
	cookie := firstVisit(t, tab)
	if !strings.HasPrefix(cookie, "yonder_18000=T0K.") {
		t.Fatalf("cookie %q should hold the token and a device id", cookie)
	}

	rec, up := approvalGate(tab, "GET", "/", http.Header{"Cookie": {cookie + "; session=abc"}})
	if rec.Code != http.StatusAccepted || rec.Header().Get("Retry-After") != "2" || up.called {
		t.Fatalf("waiting: %d Retry-After %q called %v; want 202, 2, not called", rec.Code, rec.Header().Get("Retry-After"), up.called)
	}
	if body := rec.Body.String(); !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "approve") {
		t.Errorf("waiting page lacks the refresh tag or the explanation:\n%s", body)
	}

	_, id, _ := strings.Cut(strings.TrimPrefix(cookie, "yonder_18000="), ".")
	tab.decide(id, true)
	rec, up = approvalGate(tab, "GET", "/", http.Header{"Cookie": {cookie + "; session=abc"}})
	if !up.called || rec.Code != http.StatusOK {
		t.Fatalf("approved: %d called %v; want the app to answer", rec.Code, up.called)
	}
	if got := up.req.Header.Get("Cookie"); got != "session=abc" {
		t.Errorf("the app saw Cookie %q, want only session=abc", got)
	}
}

// TestApprovalGateDeniedDevicesAreRefused checks that a denied device gets 403 and that the
// app is never called.
func TestApprovalGateDeniedDevicesAreRefused(t *testing.T) {
	tab := newDeviceTable()
	cookie := firstVisit(t, tab)
	_, id, _ := strings.Cut(strings.TrimPrefix(cookie, "yonder_18000="), ".")
	tab.decide(id, false)
	rec, up := approvalGate(tab, "GET", "/", http.Header{"Cookie": {cookie}})
	if rec.Code != http.StatusForbidden || up.called || !strings.Contains(rec.Body.String(), "did not allow") {
		t.Errorf("denied: %d %q called %v; want 403 'did not allow' and no app call", rec.Code, rec.Body.String(), up.called)
	}
}

// TestApprovalGateDoesNotForwardTheFirstPost checks that a POST that carries the token on a
// device's first visit is held back and never forwarded: forwarding it would skip approval.
func TestApprovalGateDoesNotForwardTheFirstPost(t *testing.T) {
	tab := newDeviceTable()
	rec, up := approvalGate(tab, "POST", "/api?yonder=T0K", nil)
	if up.called || rec.Code != http.StatusAccepted {
		t.Errorf("first POST: %d called %v; want 202 and no app call", rec.Code, up.called)
	}
}

// TestApprovalGateRefusesOldAndUnknownCookies checks that the plain-token cookie of a gate
// without approval, a cookie with an unknown device id, and a wrong token all get 403.
func TestApprovalGateRefusesOldAndUnknownCookies(t *testing.T) {
	tab := newDeviceTable()
	for _, cookie := range []string{"yonder_18000=T0K", "yonder_18000=T0K.nosuchdevice", "yonder_18000=WRONG.nosuchdevice"} {
		rec, up := approvalGate(tab, "GET", "/", http.Header{"Cookie": {cookie}})
		if rec.Code != http.StatusForbidden || rec.Body.String() != wantForbidden || up.called {
			t.Errorf("cookie %q: %d %q called %v; want the 403 page", cookie, rec.Code, rec.Body.String(), up.called)
		}
	}
}

// TestApprovalGateLimitsWaitingDevices checks that once maxPendingDevices devices are waiting,
// a new visitor with the link gets 429 instead of another prompt.
func TestApprovalGateLimitsWaitingDevices(t *testing.T) {
	tab := newDeviceTable()
	for i := 0; i < maxPendingDevices; i++ { // one device from each of ten addresses
		if rec, _ := approvalGateFrom(tab, fmt.Sprintf("10.1.0.%d:51000", i+1), "GET", "/?yonder=T0K", nil); rec.Code != http.StatusFound {
			t.Fatalf("device %d: %d, want 302", i, rec.Code)
		}
	}
	rec, up := approvalGateFrom(tab, "10.1.9.9:51000", "GET", "/?yonder=T0K", nil)
	if rec.Code != http.StatusTooManyRequests || up.called || !strings.Contains(rec.Body.String(), "Too many devices") {
		t.Errorf("over the limit: %d %q called %v; want 429", rec.Code, rec.Body.String(), up.called)
	}
}

// TestApprovalGateRevisitingWithTheLinkKeepsTheDevice checks that opening the link again from
// an already approved browser lets it straight in and does not start a second approval.
func TestApprovalGateRevisitingWithTheLinkKeepsTheDevice(t *testing.T) {
	tab := newDeviceTable()
	cookie := firstVisit(t, tab)
	_, id, _ := strings.Cut(strings.TrimPrefix(cookie, "yonder_18000="), ".")
	tab.decide(id, true)
	rec, up := approvalGate(tab, "GET", "/page?yonder=T0K", http.Header{"Cookie": {cookie}})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/page" || up.called {
		t.Fatalf("revisit: %d Location %q; want a plain redirect", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "yonder_18000" && c.Value != strings.TrimPrefix(cookie, "yonder_18000=") {
			t.Errorf("revisit replaced the device cookie with %q", c.Value)
		}
	}
	if n := tab.waiting(); n != 0 {
		t.Errorf("%d devices waiting after a revisit, want 0", n)
	}
}

// TestDeviceTableLimitsWaitingDevicesPerAddress checks that one address can have only
// maxPendingPerAddress devices waiting, so one visitor in a loop cannot use every place, while
// other addresses still get theirs.
func TestDeviceTableLimitsWaitingDevicesPerAddress(t *testing.T) {
	tab := newDeviceTable()
	for i := 0; i < maxPendingPerAddress; i++ {
		if _, ok := tab.begin(fmt.Sprintf("10.0.0.7:%d", 40000+i), "UA"); !ok {
			t.Fatalf("device %d from one address refused, limit is %d", i, maxPendingPerAddress)
		}
	}
	if _, ok := tab.begin("10.0.0.7:49999", "UA"); ok {
		t.Error("a device beyond the per-address limit was accepted")
	}
	if _, ok := tab.begin("10.0.0.8:40000", "UA"); !ok {
		t.Error("a different address was refused because of the first one")
	}
}

// TestDeviceTableDropsDevicesThatStopWaiting checks that a device which has not been seen for
// pendingTTL (a closed tab, a link-preview bot) gives up its place and its question, while a
// device that keeps coming back keeps both.
func TestDeviceTableDropsDevicesThatStopWaiting(t *testing.T) {
	tab := newDeviceTable()
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tab.now = func() time.Time { return clock }
	gone, _ := tab.begin("10.0.0.1:1", "UA")
	stays, _ := tab.begin("10.0.0.2:1", "UA")

	clock = clock.Add(pendingTTL - time.Second)
	tab.touch(stays) // the waiting page reloaded
	clock = clock.Add(2 * time.Second)

	if _, ok := tab.begin("10.0.0.3:1", "UA"); !ok { // any new arrival triggers the clean-up
		t.Fatal("a new device was refused")
	}
	if _, known := tab.state(gone); known {
		t.Error("a device that stopped waiting is still in the table")
	}
	if st, known := tab.state(stays); !known || st != devPending {
		t.Errorf("a device that kept waiting: state %v, known %v; want pending", st, known)
	}
	if n := tab.waiting(); n != 2 {
		t.Errorf("%d waiting, want 2", n)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	first, second := tab.next(ctx), tab.next(ctx) // the two devices that are still waiting
	if first == nil || second == nil {
		t.Fatal("the owner would be asked about fewer devices than are waiting")
	}
	if first.id == gone || second.id == gone {
		t.Error("the owner would be asked about a device that left")
	}
	if extra := tab.next(ctx); extra != nil {
		t.Errorf("the owner would be asked about a third device %q that should be gone", extra.addr)
	}
}

// TestApprovalGateFreesAPlaceWhenAWaitingDeviceLeaves checks the denial-of-service fix from the
// visitor's side: after the limit is reached, a place opens up again once a waiting device has
// been silent for pendingTTL.
func TestApprovalGateFreesAPlaceWhenAWaitingDeviceLeaves(t *testing.T) {
	tab := newDeviceTable()
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tab.now = func() time.Time { return clock }
	for i := 0; i < maxPendingPerAddress; i++ {
		firstVisit(t, tab)
	}
	if rec, _ := approvalGate(tab, "GET", "/?yonder=T0K", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the per-address limit: %d, want 429", rec.Code)
	}
	clock = clock.Add(pendingTTL + time.Second)
	if rec, _ := approvalGate(tab, "GET", "/?yonder=T0K", nil); rec.Code != http.StatusFound {
		t.Errorf("after the others left: %d, want 302 (a place is free again)", rec.Code)
	}
}

// TestApprovalGateHoldsBackEveryRouteOfAWaitingDevice checks that a device that is waiting or
// denied gets no further than the gate on any method, with a WebSocket upgrade, and with a
// token in the query on a POST.
func TestApprovalGateHoldsBackEveryRouteOfAWaitingDevice(t *testing.T) {
	tab := newDeviceTable()
	cookie := firstVisit(t, tab)
	_, id, _ := strings.Cut(strings.TrimPrefix(cookie, "yonder_18000="), ".")
	upgrade := http.Header{"Cookie": {cookie}, "Connection": {"Upgrade"}, "Upgrade": {"websocket"}}
	type try struct {
		method, target string
		hdr            http.Header
	}
	tries := []try{
		{"GET", "/", http.Header{"Cookie": {cookie}}}, {"POST", "/", http.Header{"Cookie": {cookie}}},
		{"PUT", "/x", http.Header{"Cookie": {cookie}}}, {"DELETE", "/x", http.Header{"Cookie": {cookie}}},
		{"GET", "/ws", upgrade}, {"POST", "/api?yonder=T0K", http.Header{"Cookie": {cookie}}},
	}
	for _, state := range []string{"pending", "denied"} {
		if state == "denied" {
			tab.decide(id, false)
		}
		for _, tr := range tries {
			rec, up := approvalGate(tab, tr.method, tr.target, tr.hdr)
			if up.called || (rec.Code != http.StatusAccepted && rec.Code != http.StatusForbidden) {
				t.Errorf("%s device, %s %s: %d, app called %v; want 202 or 403 and no app call", state, tr.method, tr.target, rec.Code, up.called)
			}
		}
	}
}

// TestApprovalGateRefusesMalformedDeviceCookies checks that cookies with an empty device id, an
// id with extra dots, no dot at all, or only a dot are all treated as having no access.
func TestApprovalGateRefusesMalformedDeviceCookies(t *testing.T) {
	tab := newDeviceTable()
	for _, value := range []string{"T0K.", "T0K.a.b", "T0K", ".", ".T0K", "T0K..", ""} {
		rec, up := approvalGate(tab, "GET", "/", http.Header{"Cookie": {"yonder_18000=" + value}})
		if rec.Code != http.StatusForbidden || up.called {
			t.Errorf("cookie value %q: %d, app called %v; want 403", value, rec.Code, up.called)
		}
	}
}

// TestReadLineLeavesTheRestOfTheInput checks that an answer is read one line at a time without
// reading ahead, so text typed early stays in the terminal where it can be discarded.
func TestReadLineLeavesTheRestOfTheInput(t *testing.T) {
	in := strings.NewReader("yes\nsecond\n")
	if line := readLine(in); line != "yes" {
		t.Errorf("first line = %q, want yes", line)
	}
	if in.Len() != len("second\n") {
		t.Errorf("%d bytes left, want the whole second line", in.Len())
	}
	if line := readLine(strings.NewReader("no newline at the end")); line != "no newline at the end" {
		t.Errorf("last line without a newline = %q", line)
	}
	if line := readLine(strings.NewReader("")); line != "" {
		t.Errorf("empty input gave %q", line)
	}
}

// TestPromptShowsTheAddressWithoutThePort checks that the owner is asked about the visitor's
// address only: the port changes with every connection, so showing it makes one phone look
// like many devices.
func TestPromptShowsTheAddressWithoutThePort(t *testing.T) {
	tab := newDeviceTable()
	id, _ := tab.begin("192.168.1.42:51234", "curl/8.4.0")
	var out syncBuf
	go runPrompts(t.Context(), strings.NewReader("y\n"), &out, tab)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, _ := tab.state(id); st != devPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the prompt never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if text := out.String(); !strings.Contains(text, "at 192.168.1.42 to open") || strings.Contains(text, "51234") {
		t.Errorf("prompt = %q, want the address without its port", text)
	}
}

// TestDeviceTableActivePromptNotExpired checks that a device actively presented in runPrompts
// is not removed by forgetLeavers when the operator takes longer than pendingTTL to answer.
func TestDeviceTableActivePromptNotExpired(t *testing.T) {
	tab := newDeviceTable()
	now := time.Now()
	tab.now = func() time.Time { return now }

	id, ok := tab.begin("192.168.1.10:50000", "UA")
	if !ok {
		t.Fatal("begin failed")
	}

	d := tab.next(t.Context())
	if d == nil || d.id != id {
		t.Fatalf("next = %v, want device %s", d, id)
	}

	// Advance clock past pendingTTL so forgetLeavers would normally drop it.
	now = now.Add(pendingTTL + time.Minute)
	tab.begin("192.168.1.20:50000", "UA") // triggers forgetLeavers()

	if st, known := tab.state(id); !known || st != devPending {
		t.Fatalf("state = %v, %v; want active device kept pending", st, known)
	}

	// Operator answers "n" after timeout: device is denied and group is refused.
	tab.decide(id, false)
	if st, _ := tab.state(id); st != devDenied {
		t.Fatalf("after decide state = %v, want denied", st)
	}
	if _, ok := tab.begin("192.168.1.10:50001", "UA"); ok {
		t.Fatal("refused address group was allowed to begin again")
	}
}
