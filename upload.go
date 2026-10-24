// upload.go receives files that other devices send to a shared folder. Uploads stream to a
// hidden temporary file and are renamed into place without ever overwriting an existing file,
// so a stopped or failed upload never leaves a half-written file under a real name.

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// uploads counts requests still writing, so shutdown can wait for their cleanup.
var uploads sync.WaitGroup

// waitUploads waits for in-flight uploads, at most d. It reports whether they finished.
func waitUploads(d time.Duration) bool {
	done := make(chan struct{})
	go func() { uploads.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// maxUpload caps one upload request. A var so tests can lower it.
var maxUpload int64 = 4 << 30

// receiveUpload streams every "file" part into dir, never overwriting, then
// sends the browser back to the folder listing.
func receiveUpload(w http.ResponseWriter, r *http.Request, root *os.Root, dir, back string) {
	uploads.Add(1)
	defer uploads.Done()
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "yonder: expected a file upload", http.StatusBadRequest)
		return
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			uploadError(w, err)
			return
		}
		raw := p.FileName()
		if p.FormName() != "file" || raw == "" { // empty name: nothing was selected
			continue
		}
		name, ok := uploadName(raw, runtime.GOOS == "windows")
		if !ok {
			http.Error(w, "yonder: refused file name "+fmt.Sprintf("%q", raw), http.StatusBadRequest)
			return
		}
		if err := saveUnique(root, dir, name, p); err != nil {
			uploadError(w, err)
			return
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// errBadName marks a name the filesystem refused (too long, reserved on Windows).
var errBadName = errors.New("refused file name")

// uploadError turns an upload failure into the matching HTTP error: 413 when the size limit was
// hit, 400 when the file name was refused, and 500 for anything else.
func uploadError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBadName) {
		http.Error(w, "yonder: the file name was refused", http.StatusBadRequest)
		return
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		http.Error(w, "yonder: upload too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "yonder: upload failed", http.StatusInternalServerError)
}

// uploadName keeps only the base name (after the last / or \) and applies the
// same rules as download paths, so nothing hidden or path-like is written.
func uploadName(raw string, windows bool) (string, bool) {
	name := raw[strings.LastIndexAny(raw, `/\`)+1:]
	for _, r := range name {
		// Control and bidi characters can disguise a name ("gpj.exe" shown as "exe.jpg").
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return "", false
		}
	}
	rel, ok := safeRel("/"+name, windows)
	return name, ok && rel == name
}

// saveUnique streams src into a hidden temp file (never listed or served), then
// claims dir/name, or "name (N).ext" if taken, with O_EXCL and renames the temp
// over that fresh empty placeholder. Existing files are never touched, and a
// half-finished upload never shows under a real name.
func saveUnique(root *os.Root, dir, name string, src io.Reader) error {
	tmp := path.Join(dir, ".yonder-part-"+newToken())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = root.Remove(tmp) // best effort: the write already failed, and the caller reports that error
		return err
	}

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		final := path.Join(dir, n)
		placeholder, err := root.OpenFile(final, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			_ = root.Remove(tmp)
			if isBadNameErr(err) {
				return fmt.Errorf("%w: %w", errBadName, err)
			}
			return err
		}
		placeholder.Close()
		if err := root.Rename(tmp, final); err != nil {
			_ = root.Remove(tmp)
			_ = root.Remove(final) // the empty placeholder we just created
			return err
		}
		return nil
	}
	_ = root.Remove(tmp)
	return errors.New("too many files with this name")
}

// isBadNameErr reports errors that mean the name itself was refused, not a full disk
// or an exhausted file table.
func isBadNameErr(err error) bool {
	if errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.EINVAL) || errors.Is(err, fs.ErrInvalid) {
		return true
	}
	if runtime.GOOS == "windows" {
		// ERROR_INVALID_NAME, ERROR_BAD_PATHNAME, ERROR_FILENAME_EXCED_RANGE
		for _, code := range []syscall.Errno{123, 161, 206} {
			if errors.Is(err, code) {
				return true
			}
		}
	}
	return false
}
