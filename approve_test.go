// approve_test.go tests device approval (--approve): the table that remembers each device,
// the terminal questions, how the access gate treats pending, approved and denied devices,
// and that nothing about a visitor's browser can reach the terminal unfiltered.

package main

import (
	"fmt"
	"io"
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
		id, ok := tab.begin("10.0.0.1:1", "UA")
		if !ok {
			t.Fatalf("device %d refused, limit is %d", i, maxPendingDevices)
		}
		if i == 0 {
			first = id
		}
	}
	if _, ok := tab.begin("10.0.0.1:1", "UA"); ok {
		t.Fatal("a device beyond the limit was accepted")
	}
	tab.decide(first, true)
	if _, ok := tab.begin("10.0.0.1:1", "UA"); !ok {
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
	for _, part := range []string{"Allow Safari on an iPhone at 192.168.1.10:50000 to open this? [y/N]", "192.168.1.13:50000"} {
		if !strings.Contains(text, part) {
			t.Errorf("prompt output lacks %q:\n%s", part, text)
		}
	}
}

// approvalGate sends one request through a gate with device approval on, and returns the
// gate's response and the stand-in app behind it.
func approvalGate(tab *deviceTable, method, target string, hdr http.Header) (*httptest.ResponseRecorder, *recorder) {
	up := &recorder{}
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "192.168.1.42:51000"
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
	for i := 0; i < maxPendingDevices; i++ {
		firstVisit(t, tab)
	}
	rec, up := approvalGate(tab, "GET", "/?yonder=T0K", nil)
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

// TestApprovalWaitingPageNeverShowsTheToken checks that the page a waiting device sees does not
// contain the share token, which only the owner and the people they invited should know.
func TestApprovalWaitingPageNeverShowsTheToken(t *testing.T) {
	tab := newDeviceTable()
	cookie := firstVisit(t, tab)
	rec, _ := approvalGate(tab, "GET", "/", http.Header{"Cookie": {cookie}})
	body, _ := io.ReadAll(rec.Result().Body)
	if strings.Contains(string(body), "T0K") {
		t.Error("the waiting page contains the token")
	}
}
