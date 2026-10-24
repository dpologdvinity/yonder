//go:build !windows

package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
	_ = os.Remove
}
