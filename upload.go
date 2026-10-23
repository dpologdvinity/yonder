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
)

// maxUpload caps one upload request. A var so tests can lower it.
var maxUpload int64 = 4 << 30

// receiveUpload streams every "file" part into dir, never overwriting, then
// sends the browser back to the folder listing.
func receiveUpload(w http.ResponseWriter, r *http.Request, root *os.Root, dir, back string) {
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

func uploadError(w http.ResponseWriter, err error) {
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
	if strings.ContainsRune(name, 0) {
		return "", false
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
		root.Remove(tmp)
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
		if err == nil {
			placeholder.Close()
			err = root.Rename(tmp, final)
		}
		if err != nil {
			root.Remove(tmp)
		}
		return err
	}
	root.Remove(tmp)
	return errors.New("too many files with this name")
}
