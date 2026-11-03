package server

import (
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxWorkspaceRead   = 2 << 20
	maxWorkspaceUpload = 512 << 20
)

type wsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func (s *Server) workspace() workspaceStore { return workspaceStore{root: filepath.Clean(s.m.dir)} }
func (s *Server) wsResolve(input string) (string, bool) {
	location, problem := s.workspace().locate(input)
	return location.absolute, problem == nil
}
func (s *Server) wsRel(absolute string) string { return s.workspace().relative(absolute) }

// HTTP owns authentication/routing, JSON decoding, multipart limits and streaming.
// The store owns resolved locations, filesystem actions and their error policy.
func workspaceResponse(w http.ResponseWriter, payload any, problem *workspaceFault) {
	if problem != nil {
		writeErr(w, problem.status, problem.err.Error())
		return
	}
	writeJSON(w, 200, payload)
}
func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	result, problem := s.workspace().list(r.URL.Query().Get("path"))
	workspaceResponse(w, result, problem)
}
func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	result, problem := s.workspace().preview(r.URL.Query().Get("path"))
	workspaceResponse(w, result, problem)
}
func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &input); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	relative, problem := s.workspace().write(input.Path, input.Content)
	workspaceResponse(w, map[string]any{"ok": true, "path": relative}, problem)
}
func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if err := decode(r, &input); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	relative, problem := s.workspace().mkdir(input.Path)
	workspaceResponse(w, map[string]any{"ok": true, "path": relative}, problem)
}
func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	workspaceResponse(w, map[string]any{"ok": true}, s.workspace().remove(r.URL.Query().Get("path")))
}
func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	location, problem := s.workspace().download(r.URL.Query().Get("path"))
	if problem != nil {
		workspaceResponse(w, nil, problem)
		return
	}
	name := filepath.Base(location.absolute)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeFile(w, r, location.absolute)
}
func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	store := s.workspace()
	directory, problem := store.uploadDirectory(r.URL.Query().Get("path"))
	if problem != nil {
		workspaceResponse(w, nil, problem)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "failed to parse upload or size limit exceeded: "+err.Error())
		return
	}
	count, problem := store.upload(directory, r.MultipartForm.File["file"])
	workspaceResponse(w, map[string]any{"uploaded": count}, problem)
}

func saveUpload(hdr *multipart.FileHeader, dest string) error {
	src, err := hdr.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename token.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}
