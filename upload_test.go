package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type part struct{ name, body string }

func postFiles(h http.Handler, target string, parts ...part) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		fw, _ := mw.CreateFormFile("file", p.name)
		_, _ = fw.Write([]byte(p.body))
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// uploadDir returns a shared folder with index.txt and sub/, plus its os.Root.
func uploadDir(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "index.txt"), []byte("original"), 0o644))
	must(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return dir, root
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestUploadSaves(t *testing.T) {
	dir, root := uploadDir(t)
	h := newFileServer(root, "", true)
	rec := postFiles(h, "/", part{"a.txt", "aaa"}, part{"b.txt", "bbb"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("got %d Location %q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}
	if readFile(t, filepath.Join(dir, "a.txt")) != "aaa" || readFile(t, filepath.Join(dir, "b.txt")) != "bbb" {
		t.Error("uploaded contents wrong")
	}
}

func TestUploadSubfolder(t *testing.T) {
	dir, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "", true), "/sub/", part{"c.txt", "c"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/sub/" {
		t.Fatalf("got %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if readFile(t, filepath.Join(dir, "sub", "c.txt")) != "c" {
		t.Error("file not in sub/")
	}
}

func TestUploadNoOverwrite(t *testing.T) {
	dir, root := uploadDir(t)
	h := newFileServer(root, "", true)
	postFiles(h, "/", part{"index.txt", "new1"})
	postFiles(h, "/", part{"index.txt", "new2"})
	if readFile(t, filepath.Join(dir, "index.txt")) != "original" {
		t.Error("original overwritten")
	}
	if readFile(t, filepath.Join(dir, "index (1).txt")) != "new1" || readFile(t, filepath.Join(dir, "index (2).txt")) != "new2" {
		t.Error("renamed copies wrong")
	}
}

func TestUploadPathNameUsesBase(t *testing.T) {
	dir, root := uploadDir(t)
	postFiles(newFileServer(root, "", true), "/", part{"../../evil.txt", "e"}, part{`C:\Users\x\photo.jpg`, "p"})
	if readFile(t, filepath.Join(dir, "evil.txt")) != "e" || readFile(t, filepath.Join(dir, "photo.jpg")) != "p" {
		t.Error("base names not used")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dir)), "evil.txt")); err == nil {
		t.Error("file escaped the folder")
	}
}

func TestUploadRejectsBadNames(t *testing.T) {
	for _, name := range []string{".env", "..", "a/../.hidden"} {
		dir, root := uploadDir(t)
		rec := postFiles(newFileServer(root, "", true), "/", part{name, "x"})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d, want 400", name, rec.Code)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 2 {
			t.Errorf("%q: folder now has %d entries, want 2", name, len(entries))
		}
	}
}

func TestUploadEmptySelectionIgnored(t *testing.T) {
	dir, root := uploadDir(t)
	if rec := postFiles(newFileServer(root, "", true), "/", part{"", ""}); rec.Code != http.StatusSeeOther {
		t.Errorf("got %d, want 303", rec.Code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("folder has %d entries, want 2", len(entries))
	}
}

func TestUploadDisabled(t *testing.T) {
	dir, root := uploadDir(t)
	h := newFileServer(root, "", false)
	if rec := postFiles(h, "/", part{"a.txt", "a"}); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d, want 405", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Error("file written with uploads off")
	}
	if strings.Contains(getFile(h, "/", nil).Body.String(), "<form") {
		t.Error("listing shows upload form with uploads off")
	}
	if !strings.Contains(getFile(newFileServer(root, "", true), "/", nil).Body.String(), `enctype="multipart/form-data"`) {
		t.Error("listing lacks upload form with uploads on")
	}
}

func TestUploadTooLarge(t *testing.T) {
	old := maxUpload
	maxUpload = 300
	defer func() { maxUpload = old }()
	dir, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "", true), "/", part{"big.bin", strings.Repeat("x", 1000)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, want 413", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err == nil {
		t.Error("partial file left behind")
	}
}

func TestUploadHiddenFolderRefused(t *testing.T) {
	dir, root := uploadDir(t)
	must(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	if rec := postFiles(newFileServer(root, "", true), "/.git/", part{"x.txt", "x"}); rec.Code != 404 {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestUploadCrossOriginRefused(t *testing.T) {
	for _, hdr := range []http.Header{
		{"Origin": {"http://evil.example"}},
		{"Sec-Fetch-Site": {"cross-site"}},
	} {
		dir, root := uploadDir(t)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "x.txt")
		_, _ = fw.Write([]byte("x"))
		_ = mw.Close()
		req := httptest.NewRequest("POST", "/", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		for k, v := range hdr {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		newFileServer(root, "", true).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%v: got %d, want 403", hdr, rec.Code)
		}
		if _, err := os.Stat(filepath.Join(dir, "x.txt")); err == nil {
			t.Errorf("%v: cross-origin upload was written", hdr)
		}
	}
}

func TestUploadLeavesNoPartFiles(t *testing.T) {
	dir, root := uploadDir(t)
	postFiles(newFileServer(root, "", true), "/", part{"ok.txt", "fine"})
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".yonder-part") {
			t.Errorf("temp file %s left after success", e.Name())
		}
	}
	if len(entries) != 3 {
		t.Errorf("folder has %d entries, want 3", len(entries))
	}
}

func TestUploadInProgressHidden(t *testing.T) {
	dir, root := uploadDir(t)
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	release := make(chan struct{})
	go func() {
		fw, _ := mw.CreateFormFile("file", "slow.txt")
		_, _ = fw.Write([]byte("first half"))
		<-release
		_, _ = fw.Write([]byte(" second half"))
		_ = mw.Close()
		_ = pw.Close()
	}()
	req := httptest.NewRequest("POST", "/", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	done := make(chan struct{})
	go func() { newFileServer(root, "", true).ServeHTTP(httptest.NewRecorder(), req); close(done) }()

	// Wait until the server has started writing something, then look.
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		if len(entries) > 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "slow.txt")); err == nil {
		t.Error("half-uploaded file visible under its final name")
	}
	close(release)
	<-done
	if readFile(t, filepath.Join(dir, "slow.txt")) != "first half second half" {
		t.Error("final contents wrong")
	}
}

func TestUploadBadNamesAre400(t *testing.T) {
	long := strings.Repeat("a", 300) + ".txt"
	dir, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "", true), "/", part{long, "x"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("long name: got %d, want 400", rec.Code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("folder has %d entries, want 2 (no temp file left)", len(entries))
	}
}

func TestUploadNameRejectsInvisibleText(t *testing.T) {
	for _, name := range []string{"cod\u202egpj.exe", "a\nb.txt", "x\u200f.txt", "t\u2066.txt"} {
		if _, ok := uploadName(name, false); ok {
			t.Errorf("uploadName(%q) accepted", name)
		}
	}
	if _, ok := uploadName("café 日本語.txt", false); !ok {
		t.Error("ordinary unicode name refused")
	}
}

func TestUploadWrongTargetMessage(t *testing.T) {
	_, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "", true), "/index.txt", part{"x.txt", "x"})
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), "uploads go to a folder") {
		t.Errorf("got %d %q, want 405 'uploads go to a folder'", rec.Code, rec.Body.String())
	}
}

func TestListingShowsUploadLimit(t *testing.T) {
	_, root := uploadDir(t)
	body := getFile(newFileServer(root, "", true), "/", nil).Body.String()
	if !strings.Contains(body, `data-max="4294967296"`) || !strings.Contains(body, "too large") {
		t.Errorf("listing lacks the client-side size check:\n%s", body)
	}
}
