package server

import (
	"bytes"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Only resolved locations reach filesystem operations. This retains the original
// lexical path policy; it does not claim to resolve symlinks or provide a sandbox.
type workspaceStore struct{ root string }
type workspaceLocation struct {
	absolute string
	relative string
	root     bool
}
type workspaceFault struct {
	status int
	err    error
}

func workspaceProblem(status int, text string) *workspaceFault {
	return &workspaceFault{status: status, err: errors.New(text)}
}
func workspaceIO(err error) *workspaceFault {
	if err == nil {
		return nil
	}
	return &workspaceFault{status: 500, err: err}
}
func (store workspaceStore) relative(absolute string) string {
	value, err := filepath.Rel(store.root, absolute)
	if err != nil || value == "." {
		return ""
	}
	return filepath.ToSlash(value)
}
func (store workspaceStore) locate(input string) (workspaceLocation, *workspaceFault) {
	input = strings.TrimPrefix(strings.TrimSpace(input), "/")
	absolute := filepath.Clean(filepath.Join(store.root, filepath.Clean("/"+input)))
	if absolute != store.root && !strings.HasPrefix(absolute, store.root+string(os.PathSeparator)) {
		return workspaceLocation{}, workspaceProblem(400, "invalid path")
	}
	return workspaceLocation{absolute: absolute, relative: store.relative(absolute), root: absolute == store.root}, nil
}

type workspaceDirectory struct {
	Path    string    `json:"path"`
	Entries []wsEntry `json:"entries"`
}
type workspacePreview struct {
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Binary   bool    `json:"binary"`
	TooLarge bool    `json:"too_large,omitempty"`
	Content  *string `json:"content,omitempty"`
}

func (store workspaceStore) list(input string) (workspaceDirectory, *workspaceFault) {
	location, problem := store.locate(input)
	if problem != nil {
		return workspaceDirectory{}, problem
	}
	info, err := os.Stat(location.absolute)
	if err != nil {
		return workspaceDirectory{}, workspaceProblem(404, "path does not exist")
	}
	if !info.IsDir() {
		return workspaceDirectory{}, workspaceProblem(400, "not a directory")
	}
	children, err := os.ReadDir(location.absolute)
	if err != nil {
		return workspaceDirectory{}, workspaceIO(err)
	}
	result := workspaceDirectory{Path: location.relative, Entries: make([]wsEntry, 0, len(children))}
	for _, child := range children {
		metadata, err := child.Info()
		if err != nil {
			continue
		}
		result.Entries = append(result.Entries, wsEntry{
			Name:  child.Name(),
			Path:  store.relative(filepath.Join(location.absolute, child.Name())),
			Dir:   child.IsDir(),
			Size:  metadata.Size(),
			MTime: metadata.ModTime().UnixMilli(),
		})
	}
	sort.Slice(result.Entries, func(left, right int) bool {
		a, b := result.Entries[left], result.Entries[right]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return result, nil
}
func (store workspaceStore) preview(input string) (workspacePreview, *workspaceFault) {
	location, problem := store.locate(input)
	if problem != nil {
		return workspacePreview{}, problem
	}
	info, err := os.Stat(location.absolute)
	if err != nil {
		return workspacePreview{}, workspaceProblem(404, "file does not exist")
	}
	if info.IsDir() {
		return workspacePreview{}, workspaceProblem(400, "is a directory and cannot be read as a file")
	}
	view := workspacePreview{Path: location.relative, Size: info.Size(), Binary: true}
	if info.Size() > maxWorkspaceRead {
		view.TooLarge = true
		return view, nil
	}
	data, err := os.ReadFile(location.absolute)
	if err != nil {
		return workspacePreview{}, workspaceIO(err)
	}
	if bytes.IndexByte(data, 0) < 0 && utf8.Valid(data) {
		text := string(data)
		view.Binary, view.Content = false, &text
	}
	return view, nil
}
func (store workspaceStore) writable(input string) (workspaceLocation, *workspaceFault) {
	location, problem := store.locate(input)
	if problem == nil && location.root {
		problem = workspaceProblem(400, "invalid path")
	}
	return location, problem
}
func (store workspaceStore) write(input, content string) (string, *workspaceFault) {
	location, problem := store.writable(input)
	if problem != nil {
		return "", problem
	}
	if info, err := os.Stat(location.absolute); err == nil && info.IsDir() {
		return "", workspaceProblem(400, "target is a directory")
	}
	if err := os.MkdirAll(filepath.Dir(location.absolute), 0o755); err != nil {
		return "", workspaceIO(err)
	}
	return location.relative, workspaceIO(os.WriteFile(location.absolute, []byte(content), 0o644))
}
func (store workspaceStore) mkdir(input string) (string, *workspaceFault) {
	location, problem := store.writable(input)
	if problem != nil {
		return "", problem
	}
	return location.relative, workspaceIO(os.MkdirAll(location.absolute, 0o755))
}
func (store workspaceStore) remove(input string) *workspaceFault {
	location, problem := store.locate(input)
	if problem != nil {
		return problem
	}
	if location.root {
		return workspaceProblem(400, "cannot delete the workspace root directory")
	}
	if _, err := os.Stat(location.absolute); err != nil {
		return workspaceProblem(404, "path does not exist")
	}
	return workspaceIO(os.RemoveAll(location.absolute))
}
func (store workspaceStore) download(input string) (workspaceLocation, *workspaceFault) {
	location, problem := store.locate(input)
	if problem != nil {
		return workspaceLocation{}, problem
	}
	if info, err := os.Stat(location.absolute); err != nil || info.IsDir() {
		return workspaceLocation{}, workspaceProblem(404, "file does not exist")
	}
	return location, nil
}
func (store workspaceStore) uploadDirectory(input string) (workspaceLocation, *workspaceFault) {
	location, problem := store.locate(input)
	if problem != nil {
		return workspaceLocation{}, problem
	}
	if info, err := os.Stat(location.absolute); err != nil || !info.IsDir() {
		return workspaceLocation{}, workspaceProblem(400, "target directory does not exist")
	}
	return location, nil
}
func (store workspaceStore) upload(directory workspaceLocation, files []*multipart.FileHeader) (int, *workspaceFault) {
	if len(files) == 0 {
		return 0, workspaceProblem(400, "missing upload file (form field file)")
	}
	saved := 0
	for _, file := range files {
		name := filepath.Base(file.Filename)
		if name == "" || name == "." || name == ".." {
			continue
		}
		location, problem := store.locate(filepath.Join(directory.relative, name))
		if problem != nil {
			continue
		}
		if err := saveUpload(file, location.absolute); err != nil {
			return saved, workspaceIO(err)
		}
		saved++
	}
	return saved, nil
}
