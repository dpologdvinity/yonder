package main

import (
	"fmt"
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
	t.Cleanup(func() { _ = root.Close() })
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
	rec := getFile(newFileServer(fileFixture(t), "", false), "/", nil)
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
	sub, idx := strings.Index(body, ">sub/<"), strings.Index(body, ">index.txt<")
	if sub < 0 || idx < 0 || sub > idx {
		t.Errorf("folders should be listed before files (sub/ at %d, index.txt at %d)", sub, idx)
	}
	for _, hidden := range []string{".env", ".git"} {
		if strings.Contains(body, hidden) {
			t.Errorf("listing shows hidden %q", hidden)
		}
	}
}

func TestFilesServeFile(t *testing.T) {
	h := newFileServer(fileFixture(t), "", false)
	if rec := getFile(h, "/sub/b.txt", nil); rec.Code != 200 || rec.Body.String() != "bee" {
		t.Errorf("got %d %q, want 200 bee", rec.Code, rec.Body.String())
	}
	if rec := getFile(h, "/sub/b.txt", http.Header{"Range": {"bytes=0-1"}}); rec.Code != 206 || rec.Body.String() != "be" {
		t.Errorf("range: got %d %q, want 206 be", rec.Code, rec.Body.String())
	}
}

func TestFilesSubfolderRedirect(t *testing.T) {
	rec := getFile(newFileServer(fileFixture(t), "", false), "/sub", nil)
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusFound || rec.Header().Get("Location") != "/sub/" {
		t.Errorf("got %d Location %q, want redirect to /sub/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestFilesHidden(t *testing.T) {
	h := newFileServer(fileFixture(t), "", false)
	for _, p := range []string{"/.env", "/.git/config", "/.git/"} {
		if rec := getFile(h, p, nil); rec.Code != 404 {
			t.Errorf("%s: got %d, want 404", p, rec.Code)
		}
	}
}

func TestFilesNoEscape(t *testing.T) {
	h := newFileServer(fileFixture(t), "", false)
	for _, p := range []string{"/../x", "/%2e%2e/x", "/sub/../../x", "/out"} {
		rec := getFile(h, p, nil)
		if strings.Contains(rec.Body.String(), secret) || rec.Code == 200 {
			t.Errorf("%s: got %d %q, want no access", p, rec.Code, rec.Body.String())
		}
	}
}

func TestFilesSingle(t *testing.T) {
	h := newFileServer(fileFixture(t), "index.txt", false)
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
	h := newFileServer(fileFixture(t), "", false)
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
	h := newFileServer(fileFixture(t), "", false)
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

func TestFilesSingleModeRefusesUpload(t *testing.T) {
	dir, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "index.txt", true), "/", part{"x.txt", "x"})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
		t.Error("single-file share accepted an upload")
	}
}

func TestFilesTitleIsFolderName(t *testing.T) {
	root := fileFixture(t)
	h := newFileServer(root, "", false)
	want := filepath.Base(root.Name())
	if body := getFile(h, "/", nil).Body.String(); !strings.Contains(body, "<title>"+want+"</title>") || !strings.Contains(body, "<h1>"+want+"</h1>") {
		t.Errorf("root listing title not %q:\n%s", want, body)
	}
	if body := getFile(h, "/sub/", nil).Body.String(); !strings.Contains(body, "<title>sub</title>") {
		t.Errorf("subfolder title not sub:\n%s", body)
	}
}

func TestFilesListingCap(t *testing.T) {
	old := maxListing
	maxListing = 3
	defer func() { maxListing = old }()
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.txt", i)), []byte("x"), 0o644))
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	body := getFile(newFileServer(root, "", false), "/", nil).Body.String()
	if !strings.Contains(body, "this folder has more") || strings.Count(body, `href="./f`) != 3 {
		t.Errorf("listing not capped at 3:\n%s", body)
	}
}

func TestFilesCapCountsOnlyVisible(t *testing.T) {
	old := maxListing
	maxListing = 3
	defer func() { maxListing = old }()
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf(".hidden%d", i)), []byte("x"), 0o644))
	}
	for i := 0; i < 3; i++ {
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("v%d.txt", i)), []byte("x"), 0o644))
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	body := getFile(newFileServer(root, "", false), "/", nil).Body.String()
	if strings.Contains(body, "has more") || strings.Count(body, `href="./v`) != 3 {
		t.Errorf("hidden files wrongly counted toward the cap:\n%s", body)
	}
}

func TestFilesSingleModeListsItsFileInBigFolder(t *testing.T) {
	old := maxListing
	maxListing = 3
	defer func() { maxListing = old }()
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("a%d.txt", i)), []byte("x"), 0o644))
	}
	must(t, os.WriteFile(filepath.Join(dir, "zzz-the-one.txt"), []byte("x"), 0o644))
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	body := getFile(newFileServer(root, "zzz-the-one.txt", false), "/", nil).Body.String()
	if !strings.Contains(body, `href="./zzz-the-one.txt"`) || strings.Contains(body, "has more") {
		t.Errorf("single-file listing lost its file:\n%s", body)
	}
}

func TestFilesUploadModeSandboxesContent(t *testing.T) {
	_, root := uploadDir(t)
	rec := getFile(newFileServer(root, "", true), "/index.txt", nil)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("headers = %v, want nosniff and CSP sandbox when uploads are on", rec.Header())
	}
	rec = getFile(newFileServer(root, "", false), "/index.txt", nil)
	if rec.Header().Get("Content-Security-Policy") != "" {
		t.Error("CSP sandbox set with uploads off; it would break shared static sites")
	}
}

func TestVisibleName(t *testing.T) {
	tests := []struct {
		name    string
		windows bool
		want    bool
	}{
		{"a.txt", false, true}, {".env", false, false}, {"a~1.txt", true, false},
		{"a~1.txt", false, true}, {"a:b", true, false},
	}
	for _, tt := range tests {
		if got := visibleName(tt.name, tt.windows); got != tt.want {
			t.Errorf("visibleName(%q, %v) = %v, want %v", tt.name, tt.windows, got, tt.want)
		}
	}
}

func TestFilesCapKeepsAlphabeticalFirst(t *testing.T) {
	old := maxListing
	maxListing = 3
	defer func() { maxListing = old }()
	dir := t.TempDir()
	for i := 39; i >= 0; i-- { // created in reverse, so disk order is not alphabetical
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o644))
	}
	root, _ := os.OpenRoot(dir)
	defer func() { _ = root.Close() }()
	body := getFile(newFileServer(root, "", false), "/", nil).Body.String()
	for _, want := range []string{">f00.txt<", ">f01.txt<", ">f02.txt<"} {
		if !strings.Contains(body, want) {
			t.Errorf("capped listing lacks %s:\n%s", want, body)
		}
	}
	if strings.Count(body, `href="./f`) != 3 {
		t.Errorf("want exactly 3 entries:\n%s", body)
	}
}
