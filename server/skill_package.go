package server

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Package inspection owns archive selection and naming, while the existing ZIP
// helpers retain decoding/method policy. Entry order is never sorted or filtered
// again after root selection: it determines ties, limits and the first error.
type skillPackage struct {
	entries []skillZipEntry
	name    string
	prefix  string
}

func inspectSkillPackage(compressed []byte, filename string) (skillPackage, *skillLibraryReply) {
	reader, err := newSkillZipReader(compressed)
	if err != nil {
		return skillPackage{}, skillProblem(400, err.Error())
	}
	archive := skillPackage{entries: skillZipEntries(reader)}
	if err := checkSkillZipMethods(archive.entries); err != nil {
		return archive, skillProblem(400, err.Error())
	}
	selected := -1
	for index, entry := range archive.entries {
		if path.Base(entry.name) == "SKILL.md" && (selected < 0 || strings.Count(entry.name, "/") < strings.Count(archive.entries[selected].name, "/")) {
			selected = index
		}
	}
	if selected < 0 {
		return archive, skillProblem(400, "SKILL.md not found in the archive")
	}
	manifest := archive.entries[selected]
	if directory := path.Dir(manifest.name); directory != "." {
		archive.prefix = directory + "/"
	}
	content, err := readZipEntry(manifest.f)
	if err != nil {
		return archive, skillProblem(400, "failed to read SKILL.md: "+err.Error())
	}
	archive.name = skillNameFromFrontmatter(content)
	if archive.name == "" && archive.prefix != "" {
		archive.name = path.Base(strings.TrimSuffix(archive.prefix, "/"))
	}
	if archive.name == "" {
		base := path.Base(filepath.ToSlash(filename))
		archive.name = strings.TrimSuffix(base, path.Ext(base))
	}
	if !validSkillName(archive.name) {
		return archive, skillProblem(400, "skill name is invalid (taken from the name field of SKILL.md): "+archive.name+
			"(<=64 characters, must start with a letter, using only lowercase letters/digits/hyphens or non-ASCII letters such as Chinese, with no spaces, dots, or path separators)")
	}
	return archive, nil
}

func (archive skillPackage) install(library skillLibrary, overwrite bool) *skillLibraryReply {
	target := filepath.Join(library.root, archive.name)
	if _, err := os.Stat(target); err == nil && !overwrite {
		return skillProblem(409, "skill already exists: "+archive.name+"(confirm and retry to overwrite)")
	}
	_ = os.MkdirAll(library.root, 0o755)
	staging, err := os.MkdirTemp(library.root, ".upload-*")
	if err != nil {
		return skillFailure(err)
	}
	defer os.RemoveAll(staging)
	count, failure := archive.extract(staging)
	if failure != nil {
		return failure
	}
	if _, err := os.Stat(filepath.Join(staging, "SKILL.md")); err != nil {
		return skillProblem(400, "SKILL.md is missing after extraction")
	}
	// Preserve the original delete/rename boundary and ignored removal error.
	// Successful staging is not a crash-safe transaction with the old directory.
	if overwrite {
		_ = os.RemoveAll(target)
	}
	if err := os.Rename(staging, target); err != nil {
		return skillProblem(500, "install failed: "+err.Error())
	}
	return &skillLibraryReply{status: 201, document: map[string]any{"name": archive.name, "files": count}}
}

func (archive skillPackage) extract(staging string) (int, *skillLibraryReply) {
	count, total := 0, int64(0)
	for _, entry := range archive.entries {
		if archive.prefix != "" && !strings.HasPrefix(entry.name, archive.prefix) {
			continue
		}
		relative := strings.TrimPrefix(entry.name, archive.prefix)
		if relative == "" {
			continue
		}
		clean, problem := skillRelPath(relative)
		if problem != "" {
			return count, skillProblem(400, "archive contains an illegal path "+entry.name+": "+problem)
		}
		count++
		if count > maxSkillEntries {
			return count, skillProblem(400, "too many files in the archive")
		}
		if entry.f.UncompressedSize64 > maxSkillFileBytes {
			return count, skillProblem(400, "file too large: "+relative)
		}
		bytes, err := extractSkillEntry(entry, filepath.Join(staging, clean))
		if err != nil {
			return count, skillFailure(err)
		}
		total += bytes
		if total > maxSkillTotalBytes {
			return count, skillProblem(400, "the archive is too large after extraction")
		}
	}
	return count, nil
}

func extractSkillEntry(entry skillZipEntry, destination string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, err
	}
	input, err := entry.f.Open()
	if err != nil {
		return 0, err
	}
	output, err := os.Create(destination)
	if err != nil {
		input.Close()
		return 0, err
	}
	copied, copyErr := io.Copy(output, io.LimitReader(input, maxSkillFileBytes+1))
	output.Close()
	input.Close()
	return copied, copyErr
}
