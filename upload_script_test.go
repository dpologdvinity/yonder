// upload_script_test.go runs the listing page's inline script in Node.js to check the in-
// browser upload size warning. The test is skipped when Node.js is not installed.

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// runSizeCheck executes the listing page's inline script in node with a fake form
// holding files of the given sizes, and reports whether it blocked the submit.
func runSizeCheck(t *testing.T, sizes []int64) (prevented bool, message string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	_, root := uploadDir(t)
	body := getFile(newFileServer(root, "", true), "/", nil).Body.String()
	m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no inline script in the listing:\n%s", body)
	}
	files := make([]string, len(sizes))
	for i, n := range sizes {
		files[i] = fmt.Sprintf("{size:%d}", n)
	}
	js := fmt.Sprintf(`
let handler; let alerted = null; let prevented = false;
const form = {dataset: {max: '%d'}, file: {files: [%s]}, addEventListener: (t, h) => { handler = h; }};
global.document = {querySelectorAll: () => [form]};
global.alert = (m) => { alerted = m; };
%s
handler({preventDefault: () => { prevented = true; }});
console.log(JSON.stringify({prevented, alerted}));`, maxUpload, strings.Join(files, ","), m[1])
	out, err := exec.Command(node, "-e", js).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var res struct {
		Prevented bool
		Alerted   *string
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("bad node output %q: %v", out, err)
	}
	if res.Alerted != nil {
		message = *res.Alerted
	}
	return res.Prevented, message
}

// TestUploadPageSizeCheck checks that the page warns before an upload when the files plus the
// form overhead would go over the server's limit, and does not warn below it.
func TestUploadPageSizeCheck(t *testing.T) {
	limit := maxUpload
	tests := []struct {
		name    string
		sizes   []int64
		blocked bool
	}{
		{"small", []int64{1000, 2000}, false},
		{"just under, with room for the form overhead", []int64{limit - 10000}, false},
		{"just under, but the form overhead tips it over", []int64{limit - 2000}, true},
		{"exactly the limit", []int64{limit}, true},
		{"many files whose overhead adds up", []int64{limit/2 - 1000, limit/2 - 1000}, true},
	}
	for _, tt := range tests {
		got, msg := runSizeCheck(t, tt.sizes)
		if got != tt.blocked {
			t.Errorf("%s: blocked = %v, want %v", tt.name, got, tt.blocked)
		}
		if tt.blocked && !strings.Contains(msg, "too large") || tt.blocked && !strings.Contains(msg, "limit 4 GB") {
			t.Errorf("%s: message = %q, want 'too large ... limit 4 GB'", tt.name, msg)
		}
	}
}
