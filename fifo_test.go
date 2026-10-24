// fifo_test.go checks that special files such as named pipes are neither listed nor opened. It
// does not build on Windows, which has no named pipes in the file system.

//go:build !windows

package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFilesSkipsSpecialFiles checks that a named pipe is left out of the listing and that
// asking for it gives 404 at once instead of blocking the request forever.
func TestFilesSkipsSpecialFiles(t *testing.T) {
	dir, root := uploadDir(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skip("no fifo support")
	}
	h := newFileServer(root, "", false)
	if strings.Contains(getFile(h, "/", nil).Body.String(), "pipe") {
		t.Error("listing shows a FIFO")
	}
	done := make(chan *http.Response, 1)
	go func() { rec := getFile(h, "/pipe", nil); done <- &http.Response{StatusCode: rec.Code} }()
	select {
	case r := <-done:
		if r.StatusCode != 404 {
			t.Errorf("/pipe: got %d, want 404", r.StatusCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request for a FIFO blocked")
	}
}
