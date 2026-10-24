// upload_test.go tests receiving uploads in upload.go: where files land, what is refused, and
// what is left behind (nothing) when an upload fails or is cut short.

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// part is one file in an upload request: its claimed name and its content.
type part struct{ name, body string }

// postFiles posts the parts to target as a browser form upload and returns the recorded
// response.
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

// readFile returns the content of the file at p and fails the test if it cannot be read.
func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestUploadSaves checks that uploaded files are saved in the folder and that the browser is
// sent back to the listing.
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

// TestUploadSubfolder checks that an upload from a subfolder's page lands in that subfolder.
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

// TestUploadNoOverwrite checks that a file with an existing name is saved as "name (1)" and
// that the original stays untouched.
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

// TestUploadPathNameUsesBase checks that only the last part of a name is used, so a path cannot
// move a file elsewhere.
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

// TestUploadRejectsBadNames checks that hidden names and dot-dot names are refused with 400 and
// nothing is written.
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

// TestUploadEmptySelectionIgnored checks that pressing Upload with no file chosen changes
// nothing and returns to the listing.
func TestUploadEmptySelectionIgnored(t *testing.T) {
	dir, root := uploadDir(t)
	if rec := postFiles(newFileServer(root, "", true), "/", part{"", ""}); rec.Code != http.StatusSeeOther {
		t.Errorf("got %d, want 303", rec.Code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("folder has %d entries, want 2", len(entries))
	}
}

// TestUploadDisabled checks that with uploads off POST gives 405, nothing is written, and the
// page has no upload form.
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

// TestUploadTooLarge checks that an upload over the size limit gets 413 and leaves no partial
// file.
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

// TestUploadHiddenFolderRefused checks that uploading into a hidden folder gives 404.
func TestUploadHiddenFolderRefused(t *testing.T) {
	dir, root := uploadDir(t)
	must(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	if rec := postFiles(newFileServer(root, "", true), "/.git/", part{"x.txt", "x"}); rec.Code != 404 {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

// TestUploadCrossOriginRefused checks that a form posted by another website's page is refused
// with 403 and writes nothing.
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

// TestUploadLeavesNoPartFiles checks that after a successful upload no temporary file remains.
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

// TestUploadInProgressHidden checks that while an upload is still streaming, its file does not
// appear under the final name.
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

// TestUploadBadNamesAre400 checks that a name the file system refuses (too long) gives 400 and
// leaves nothing behind.
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

// TestUploadNameRejectsInvisibleText checks that names with control or text-direction
// characters, which can disguise a file's real type, are refused, while ordinary Unicode names
// are fine.
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

// TestUploadWrongTargetMessage checks that posting to a file instead of a folder gets 405 with
// a message saying that uploads go to a folder.
func TestUploadWrongTargetMessage(t *testing.T) {
	_, root := uploadDir(t)
	rec := postFiles(newFileServer(root, "", true), "/index.txt", part{"x.txt", "x"})
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), "uploads go to a folder") {
		t.Errorf("got %d %q, want 405 'uploads go to a folder'", rec.Code, rec.Body.String())
	}
}

// TestListingShowsUploadLimit checks that the listing page tells the browser the size limit, so
// it can warn before a large upload.
func TestListingShowsUploadLimit(t *testing.T) {
	_, root := uploadDir(t)
	body := getFile(newFileServer(root, "", true), "/", nil).Body.String()
	if !strings.Contains(body, `data-max="4294967296"`) || !strings.Contains(body, "too large") {
		t.Errorf("listing lacks the client-side size check:\n%s", body)
	}
}

// TestWaitUploads checks that shutdown waits for uploads still running, gives up after the
// time limit, and returns at once when none are running.
func TestWaitUploads(t *testing.T) {
	if !waitUploads(10 * time.Millisecond) {
		t.Fatal("waitUploads with no uploads running should return true at once")
	}
	uploads.Add(1)
	if waitUploads(30 * time.Millisecond) {
		t.Error("waitUploads returned true while an upload was still running")
	}
	uploads.Done()
	if !waitUploads(time.Second) {
		t.Error("waitUploads should return true once the upload is done")
	}
}

// TestIsBadNameErr checks which errors count as "the file system refused the name" (and so
// give the visitor a 400) and which are real failures such as a full disk.
func TestIsBadNameErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"name too long", &fs.PathError{Op: "open", Path: "x", Err: syscall.ENAMETOOLONG}, true},
		{"invalid argument", syscall.EINVAL, true},
		{"invalid per the fs package", fs.ErrInvalid, true},
		{"disk full", &fs.PathError{Op: "open", Path: "x", Err: syscall.ENOSPC}, false},
		{"already exists", fs.ErrExist, false},
		{"no error", nil, false},
	}
	for _, tt := range tests {
		if got := isBadNameErr(tt.err); got != tt.want {
			t.Errorf("%s: isBadNameErr = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestSaveUniqueGivesUpAfterManyCopies checks that when 1000 files already use a name and its
// numbered variants, saving another gives a clear error and leaves no temporary file.
func TestSaveUniqueGivesUpAfterManyCopies(t *testing.T) {
	dir, root := uploadDir(t)
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	for i := 1; i < 1000; i++ {
		must(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("a (%d).txt", i)), []byte("x"), 0o644))
	}
	err := saveUnique(root, ".", "a.txt", strings.NewReader("new"))
	if err == nil || !strings.Contains(err.Error(), "too many files") {
		t.Fatalf("err = %v, want 'too many files with this name'", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".yonder-part") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}

// TestUploadUnwritableFolderIs500 checks that when the folder cannot be written to, the
// visitor gets a plain 500 (a server problem, not a bad request) and nothing is left behind.
func TestUploadUnwritableFolderIs500(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("folder permissions do not stop this user from writing")
	}
	dir, root := uploadDir(t)
	must(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // so the temp dir can be removed
	rec := postFiles(newFileServer(root, "", true), "/", part{"a.txt", "x"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d, want 500", rec.Code)
	}
}
