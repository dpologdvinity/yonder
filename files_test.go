package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	write("<script>.txt", "x")
	write("a b&c.txt", "x")
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "out")); err != nil {
		t.Skip("symlinks not supported")
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
	for _, want := range []string{`<meta name="viewport" content="width=device-width, initial-scale=1">`, `&lt;script&gt;.txt`, `href="a%20b&amp;c.txt"`} {
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
