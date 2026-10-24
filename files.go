package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var listingTmpl = template.Must(template.New("list").Parse(`<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>body{font:16px system-ui,sans-serif;margin:1rem}a{display:block;padding:.6rem 0;border-bottom:1px solid #ddd;text-decoration:none}span{float:right;color:#777}</style>
</head><body><h1>{{.Title}}</h1>
{{if .Upload}}<form method="post" enctype="multipart/form-data" data-max="{{.MaxUpload}}"><input type="file" name="file" multiple> <button>Upload</button></form>
<script>document.querySelectorAll('form[data-max]').forEach(f=>f.addEventListener('submit',e=>{let t=4096;for(const x of f.file.files)t+=x.size+1024;if(t>+f.dataset.max){e.preventDefault();alert('These files are too large to upload (limit '+(+(f.dataset.max/2**30).toFixed(2))+' GB per upload).')}}))</script>
{{end}}{{if .Truncated}}<p>Showing {{.Cap}} items; this folder has more.</p>
{{end}}{{range .Entries}}<a href="{{.Href}}">{{.Name}}{{if .Size}}<span>{{.Size}}</span>{{end}}</a>
{{end}}</body></html>
`))

var crossOrigin = http.NewCrossOriginProtection()

// maxListing caps one folder listing. A var so tests can lower it.
var maxListing = 5000

// scanLimit bounds how many entries one request reads from a folder, so a folder with
// millions of files cannot make the listing page use unbounded time or memory.
const scanLimit = 100000

type listEntry struct{ Name, Href, Size string }

// newFileServer serves files from root. With only set, just that one file is reachable.
// All access goes through os.Root, so ".." and symlinks cannot leave the shared folder.
// With upload set, folder listings accept files posted from other devices.
func newFileServer(root *os.Root, only string, upload bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel, ok := safeRel(r.URL.Path, runtime.GOOS == "windows")
		if !ok || (only != "" && rel != "." && rel != only) {
			http.NotFound(w, r)
			return
		}
		// Refuse symlinks anywhere on the path: an in-folder link could point at .env.
		segs := strings.Split(rel, "/")
		for i := range segs {
			if rel == "." {
				break
			}
			fi, err := root.Lstat(strings.Join(segs[:i+1], "/"))
			// Only plain folders and files: no symlinks, junctions, FIFOs or devices.
			if err != nil || (!fi.IsDir() && !fi.Mode().IsRegular()) {
				http.NotFound(w, r)
				return
			}
		}

		info, err := root.Stat(rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		dirURL := (&url.URL{Path: "/" + strings.TrimPrefix(rel, ".") + "/"}).EscapedPath()
		if strings.HasPrefix(dirURL, "//") {
			dirURL = "/"
		}
		if r.Method == http.MethodPost {
			if !upload || only != "" || !info.IsDir() {
				w.Header().Set("Allow", "GET, HEAD")
				msg := "uploads are off"
				if upload && only == "" {
					msg = "uploads go to a folder"
				}
				http.Error(w, msg, http.StatusMethodNotAllowed)
				return
			}
			// Blocks form posts from other sites (Origin / Sec-Fetch-Site). The cookie
			// alone is not enough: --open has none, and other ports on this IP are same-site.
			if err := crossOrigin.Check(r); err != nil {
				http.Error(w, "yonder: uploads from other sites are refused", http.StatusForbidden)
				return
			}
			receiveUpload(w, r, root, rel, dirURL)
			return
		}
		if info.IsDir() {
			if !strings.HasSuffix(r.URL.Path, "/") {
				// Relative links in the listing need the trailing slash.
				http.Redirect(w, r, dirURL, http.StatusMovedPermanently)
				return
			}
			serveListing(w, root, rel, only, listingTitle(root, rel, only), upload)
			return
		}
		f, err := root.Open(rel)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		if upload {
			// An uploaded .html must not run as script on yonder's origin, where the cookie
			// is shared by every port on this IP.
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}

// listingTitle names the page after the shared folder (or the subfolder being viewed).
func listingTitle(root *os.Root, rel, only string) string {
	if rel == "." {
		if only != "" {
			return only
		}
		return filepath.Base(root.Name())
	}
	return path.Base(rel)
}

func serveListing(w http.ResponseWriter, root *os.Root, rel, only, title string, upload bool) {
	dir, err := root.Open(rel)
	if err != nil {
		http.Error(w, "cannot read folder", http.StatusInternalServerError)
		return
	}
	defer dir.Close()
	var visible []fs.DirEntry
	switch {
	case only != "":
		// Single-file share: never scan the folder, it may be huge and the file might sit anywhere.
		if fi, err := root.Lstat(only); err == nil && fi.Mode().IsRegular() {
			visible = append(visible, fs.FileInfoToDirEntry(fi))
		}
	default:
		// Read in batches and count only entries we will show, so hidden files and links
		// never eat the cap.
		for len(visible) <= scanLimit {
			batch, err := dir.ReadDir(512)
			for _, e := range batch {
				if visibleName(e.Name(), runtime.GOOS == "windows") && (e.IsDir() || e.Type().IsRegular()) {
					visible = append(visible, e)
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "cannot read folder", http.StatusInternalServerError)
				return
			}
		}
	}
	// Sort first, then cut: the page shows the alphabetically first entries, folders first.
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].IsDir() != visible[j].IsDir() {
			return visible[i].IsDir()
		}
		return visible[i].Name() < visible[j].Name()
	})
	truncated := len(visible) > maxListing
	if truncated {
		visible = visible[:maxListing]
	}

	var list []listEntry
	for _, e := range visible {
		name := e.Name()
		if e.IsDir() {
			list = append(list, listEntry{Name: name + "/", Href: "./" + url.PathEscape(name) + "/"})
			continue
		}
		size := "0 B"
		if info, err := e.Info(); err == nil {
			size = humanSize(info.Size())
		}
		list = append(list, listEntry{Name: name, Href: "./" + url.PathEscape(name), Size: size})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var page bytes.Buffer // render first, so a template failure can still become a clean 500
	err = listingTmpl.Execute(&page, struct {
		Title     string
		Upload    bool
		MaxUpload int64
		Cap       int
		Truncated bool
		Entries   []listEntry
	}{title, upload, maxUpload, maxListing, truncated, list})
	if err != nil {
		http.Error(w, "cannot render folder", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(page.Bytes()) // a client that went away cannot be told anything
}

// humanSize formats bytes with a 1024 base: 0 B, 999 B, 1.0 KB, 1.5 MB.
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f, i := float64(n)/1024, 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

// safeRel turns a URL path into a root-relative name, refusing hidden segments and
// separators os.Root would treat specially. On Windows that includes drive letters
// and alternate streams (":") and 8.3 short names like GIT~1 ("~").
func safeRel(urlPath string, windows bool) (string, bool) {
	rel := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if rel == "" {
		return ".", true
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") || strings.Contains(seg, "\\") ||
			(windows && strings.ContainsAny(seg, ":~")) {
			return "", false
		}
	}
	return rel, true
}

// visibleName reports whether a directory entry may be listed: the same rules that
// decide whether its URL would be served.
func visibleName(name string, windows bool) bool {
	_, ok := safeRel("/"+name, windows)
	return ok
}
