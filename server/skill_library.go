package server

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/skill"
)

// The library borrows the existing filesystem and usage ledger. It does not
// cache SDK metadata or change runtime skill bindings when a file is edited.
type skillLibrary struct {
	root   string
	ledger *db.DB
}

type skillDraft struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	License       string   `json:"license"`
	Compatibility string   `json:"compatibility"`
	MCPs          []string `json:"mcps"`
	Instructions  string   `json:"instructions"`
}

type skillMetaEdit struct {
	MCPs          *[]string `json:"mcps"`
	Description   *string   `json:"description"`
	License       *string   `json:"license"`
	Compatibility *string   `json:"compatibility"`
}

// Locations carry names/paths validated by the established public helpers.
// This preserves native path spelling; it is not a symlink isolation boundary.
type skillFolder struct{ path string }
type skillEntry struct{ path, relative string }

func (library skillLibrary) folder(name string) (skillFolder, *skillLibraryReply) {
	if !validSkillName(name) {
		return skillFolder{}, skillProblem(400, "invalid skill name")
	}
	return skillFolder{path: filepath.Join(library.root, name)}, nil
}

func (folder skillFolder) entry(raw string) (skillEntry, *skillLibraryReply) {
	relative, problem := skillRelPath(raw)
	if problem != "" {
		return skillEntry{}, skillProblem(400, problem)
	}
	return skillEntry{path: filepath.Join(folder.path, relative), relative: relative}, nil
}

func (folder skillFolder) missing() *skillLibraryReply {
	_, err := os.Stat(folder.path)
	if os.IsNotExist(err) {
		return skillProblem(404, "skill not found")
	}
	return nil
}

func (library skillLibrary) list() *skillLibraryReply {
	_ = os.MkdirAll(library.root, 0o755)
	registry, _ := skill.LoadDir(library.root)
	metadata := make(map[string]skill.Skill)
	if registry != nil {
		for _, item := range registry.List() {
			if item.Dir != "" {
				metadata[filepath.Base(item.Dir)] = item
			}
		}
	}
	directories, err := os.ReadDir(library.root)
	if err != nil {
		return skillFailure(err)
	}
	usage := make(map[string]db.SkillStat)
	if library.ledger != nil {
		rows, readErr := library.ledger.SkillStats()
		if readErr != nil {
			log.Printf("[skills] failed to read call statistics: %v", readErr)
		}
		for _, row := range rows {
			usage[row.Skill] = row
		}
	}
	result := make([]skillFileNode, 0)
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		name := directory.Name()
		meta, stat := metadata[name], usage[name]
		agents := stat.Agents
		if _, present := usage[name]; !present {
			agents = []string{}
		}
		files, _ := walkSkillFiles(filepath.Join(library.root, name))
		if files == nil {
			files = []string{}
		}
		result = append(result, skillFileNode{
			Name: name, Description: meta.Description, License: meta.License,
			Compatibility: meta.Compatibility, MCPs: meta.MCPs, Files: files,
			Calls: stat.Calls, Tasks: stat.Tasks, Agents: agents, LastUsed: stat.LastUsed,
		})
	}
	return skillResponse(200, "skills", result)
}

func (library skillLibrary) create(draft skillDraft) *skillLibraryReply {
	if !validSkillName(draft.Name) {
		return skillProblem(400, "skill name must be 1-64 lowercase alphanumeric/hyphen characters, not starting/ending/doubling hyphens")
	}
	if strings.TrimSpace(draft.Description) == "" {
		return skillProblem(400, "description is required")
	}
	folder, _ := library.folder(draft.Name)
	if _, err := os.Stat(folder.path); err == nil {
		return skillProblem(409, "skill already exists")
	}
	if err := os.MkdirAll(folder.path, 0o755); err != nil {
		return skillFailure(err)
	}
	document, err := newSkillDocument(draft.Name, draft.Description, draft.License, draft.Compatibility, draft.MCPs, draft.Instructions)
	if err == nil {
		err = os.WriteFile(filepath.Join(folder.path, "SKILL.md"), document, 0o644)
	}
	if err != nil {
		_ = os.RemoveAll(folder.path)
		return skillFailure(err)
	}
	return skillResponse(201, "name", draft.Name)
}

func (folder skillFolder) editMetadata(edit skillMetaEdit) *skillLibraryReply {
	filename := filepath.Join(folder.path, "SKILL.md")
	original, err := os.ReadFile(filename)
	if err != nil {
		return skillProblem(404, "skill not found")
	}
	updated, err := rewriteSkillFrontmatter(original, edit.MCPs, edit.Description, edit.License, edit.Compatibility)
	if err != nil {
		return skillProblem(400, err.Error())
	}
	return skillWritten(os.WriteFile(filename, updated, 0o644))
}

func (folder skillFolder) files() *skillLibraryReply {
	if failure := folder.missing(); failure != nil {
		return failure
	}
	files, err := walkSkillFiles(folder.path)
	if err != nil {
		return skillFailure(err)
	}
	if files == nil {
		files = []string{}
	}
	return skillResponse(200, "files", files)
}

func (entry skillEntry) read() *skillLibraryReply {
	content, err := os.ReadFile(entry.path)
	if os.IsNotExist(err) {
		return skillProblem(404, "file not found")
	}
	if err != nil {
		return skillFailure(err)
	}
	return &skillLibraryReply{status: 200, document: map[string]any{"content": string(content), "file": entry.relative}}
}

func (folder skillFolder) write(entry skillEntry, content string) *skillLibraryReply {
	if failure := folder.missing(); failure != nil {
		return failure
	}
	if err := os.MkdirAll(filepath.Dir(entry.path), 0o755); err != nil {
		return skillFailure(err)
	}
	return skillWritten(os.WriteFile(entry.path, []byte(content), 0o644))
}

func (folder skillFolder) createDirectory(entry skillEntry) *skillLibraryReply {
	if failure := folder.missing(); failure != nil {
		return failure
	}
	if err := os.MkdirAll(entry.path, 0o755); err != nil {
		return skillFailure(err)
	}
	return skillResponse(201, "dir", entry.relative)
}

func (entry skillEntry) remove() *skillLibraryReply {
	if _, err := os.Stat(entry.path); os.IsNotExist(err) {
		return skillProblem(404, "not found")
	}
	if err := os.RemoveAll(entry.path); err != nil {
		return skillFailure(err)
	}
	return skillResponse(200, "deleted", entry.relative)
}

func (folder skillFolder) remove(name string, ledger *db.DB) *skillLibraryReply {
	// Keep the existing partial-failure boundary: remove grants before files.
	if err := ledger.DeleteSkillVisibility(name); err != nil {
		return skillFailure(err)
	}
	if err := os.RemoveAll(folder.path); err != nil {
		return skillFailure(err)
	}
	return skillResponse(200, "deleted", name)
}
