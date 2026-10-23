package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const secret = "TOP SECRET"

// fileFixture builds a shared folder with hidden files, odd names and an escaping symlink.
func fileFixture(t *testing.T) *os.Root {
	t.Helper()
	dir := t.TempDir()
	outside := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.txt", "index")
	write("sub/b.txt", "bee")
	write(".env", secret)
	write(".git/config", secret)
	write("a b&c.txt", "x")
	if runtime.GOOS != "windows" { // Windows forbids < > : ? in names
		write("<script>.txt", "x")
		write("foo:bar.txt", "colon")
		write("a?b/inner.txt", "q")
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "out")); err != nil {
		t.Skip("symlinks not supported")
	}
	if err := os.Symlink(".env", filepath.Join(dir, "envlink")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func getFile(h http.Handler, target string, hdr http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFilesListing(t *testing.T) {
	rec := getFile(newFileServer(fileFixture(t), ""), "/", nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %q, want 200 text/html", rec.Code, rec.Header().Get("Content-Type"))
	}
	wants := []string{`<meta name="viewport" content="width=device-width, initial-scale=1">`, `href="./a%20b&amp;c.txt"`}
	if runtime.GOOS != "windows" {
		wants = append(wants, `&lt;script&gt;.txt`)
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("listing lacks %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "sub/") > strings.Index(body, "index.txt") {
		t.Error("folders should be listed before files")
	}
	for _, hidden := range []string{".env", ".git"} {
		if strings.Contains(body, hidden) {
			t.Errorf("listing shows hidden %q", hidden)
		}
	}
}

func TestFilesServeFile(t *testing.T) {
	h := newFileServer(fileFixture(t), "")
	if rec := getFile(h, "/sub/b.txt", nil); rec.Code != 200 || rec.Body.String() != "bee" {
		t.Errorf("got %d %q, want 200 bee", rec.Code, rec.Body.String())
	}
	if rec := getFile(h, "/sub/b.txt", http.Header{"Range": {"bytes=0-1"}}); rec.Code != 206 || rec.Body.String() != "be" {
		t.Errorf("range: got %d %q, want 206 be", rec.Code, rec.Body.String())
	}
}

func TestFilesSubfolderRedirect(t *testing.T) {
	rec := getFile(newFileServer(fileFixture(t), ""), "/sub", nil)
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sub/" {
		t.Errorf("got %d Location %q, want redirect to /sub/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestFilesHidden(t *testing.T) {
	h := newFileServer(fileFixture(t), "")
	for _, p := range []string{"/.env", "/.git/config", "/.git/"} {
		if rec := getFile(h, p, nil); rec.Code != 404 {
			t.Errorf("%s: got %d, want 404", p, rec.Code)
		}
	}
}

func TestFilesNoEscape(t *testing.T) {
	h := newFileServer(fileFixture(t), "")
	for _, p := range []string{"/../x", "/%2e%2e/x", "/sub/../../x", "/out"} {
		rec := getFile(h, p, nil)
		if strings.Contains(rec.Body.String(), secret) || rec.Code == 200 {
			t.Errorf("%s: got %d %q, want no access", p, rec.Code, rec.Body.String())
		}
	}
}

func TestFilesSingle(t *testing.T) {
	h := newFileServer(fileFixture(t), "index.txt")
	if rec := getFile(h, "/index.txt", nil); rec.Code != 200 || rec.Body.String() != "index" {
		t.Errorf("/index.txt: got %d %q", rec.Code, rec.Body.String())
	}
	if rec := getFile(h, "/sub/b.txt", nil); rec.Code != 404 {
		t.Errorf("/sub/b.txt: got %d, want 404", rec.Code)
	}
	body := getFile(h, "/", nil).Body.String()
	if !strings.Contains(body, "index.txt") || strings.Contains(body, "sub/") {
		t.Errorf("single-file listing = %s", body)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1024: "1.0 KB", 1536 * 1024: "1.5 MB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFilesSymlinksRefused(t *testing.T) {
	h := newFileServer(fileFixture(t), "")
	for _, p := range []string{"/envlink", "/out"} {
		if rec := getFile(h, p, nil); rec.Code != 404 || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s: got %d %q, want 404", p, rec.Code, rec.Body.String())
		}
	}
	body := getFile(h, "/", nil).Body.String()
	for _, name := range []string{"envlink", ">out"} {
		if strings.Contains(body, name) {
			t.Errorf("listing shows symlink %q", name)
		}
	}
}

func TestFilesOddNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows forbids : and ? in file names")
	}
	h := newFileServer(fileFixture(t), "")
	body := getFile(h, "/", nil).Body.String()
	if !strings.Contains(body, `href="./foo:bar.txt"`) {
		t.Errorf("colon name link broken:\n%s", body)
	}
	if rec := getFile(h, "/foo:bar.txt", nil); rec.Body.String() != "colon" {
		t.Errorf("/foo:bar.txt: got %d %q", rec.Code, rec.Body.String())
	}
	if rec := getFile(h, "/a%3Fb", nil); rec.Header().Get("Location") != "/a%3Fb/" {
		t.Errorf("folder redirect Location = %q, want /a%%3Fb/", rec.Header().Get("Location"))
	}
}

func TestSafeRel(t *testing.T) {
	tests := []struct {
		path    string
		windows bool
		want    string
		ok      bool
	}{
		{"/", false, ".", true},
		{"/sub/b.txt", false, "sub/b.txt", true},
		{"/.env", false, "", false},
		{"/sub\\..\\.env", false, "", false},
		{"/C:/x", true, "", false},
		{"/foo:bar.txt", false, "foo:bar.txt", true},
		{"/GIT~1/config", true, "", false},
		{"/backup~", false, "backup~", true},
	}
	for _, tt := range tests {
		got, ok := safeRel(tt.path, tt.windows)
		if got != tt.want || ok != tt.ok {
			t.Errorf("safeRel(%q, %v) = %q, %v; want %q, %v", tt.path, tt.windows, got, ok, tt.want, tt.ok)
		}
	}
}
