//go:build embedui

package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// embeddedHTTP retains a sub-filesystem for each assembled frontend handler.
// Requests and regular-file probes keep the original ordered fallback and
// open/stat/close behavior; ServeFileFS remains the response authority.
type embeddedHTTP struct {
	root     fs.FS
	segments map[string]string
}

func newEmbeddedApplication(bundle fs.FS) http.Handler {
	root, err := fs.Sub(bundle, "webui/dist")
	if err != nil {
		panic(err)
	}
	return embeddedHTTP{root: root, segments: exportSegmentAliases(root)}
}

func (exported embeddedHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root := exported.root

	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	// 1) exact file (assets: _next/*, favicon.ico, ...)
	// 2) route dir → <p>/index.html (trailingSlash export)
	// 3) <p>.html
	// 4) SPA fallback → index.html (hand off to client-side routing)
	for _, cand := range []string{p, p + "/index.html", p + ".html"} {
		if serveFileIfExists(w, r, root, cand) {
			return
		}
	}
	// Next's segment-prefetch URLs flatten the exported __next.* directories.
	// Exact files and page candidates above retain priority. Only a unique
	// indexed file can satisfy a logical segment URL; never synthesize data.
	if segment, ok := exported.segments[p]; ok {
		w.Header().Set("Content-Type", "text/x-component")
		http.ServeFileFS(w, r, root, segment)
		return
	}
	http.ServeFileFS(w, r, root, "index.html")
}

type exportedFile struct {
	filesystem fs.FS
	name       string
}

func (file exportedFile) serve(w http.ResponseWriter, r *http.Request) bool {
	fsys, name := file.filesystem, file.name

	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	st, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil || st.IsDir() {
		return false
	}
	http.ServeFileFS(w, r, fsys, name)
	return true
}
