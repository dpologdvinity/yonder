package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type part struct{ name, body string }

func postFiles(h http.Handler, target string, parts ...part) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		fw, _ := mw.CreateFormFile("file", p.name)
		fw.Write([]byte(p.body))
	}
	mw.Close()
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
	os.WriteFile(filepath.Join(dir, "index.txt"), []byte("original"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
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
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	if rec := postFiles(newFileServer(root, "", true), "/.git/", part{"x.txt", "x"}); rec.Code != 404 {
		t.Errorf("got %d, want 404", rec.Code)
	}
}
