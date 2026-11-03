package stackupdate

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type baseline struct {
	Source  string `json:"source"`
	Compose model  `json:"compose"`
}

// Freeze the installed source before the operator edits or pulls the checkout.
// Recovery must use the old bootstrap/configuration, not today's working files.
// This uses local Git objects or the exported file manifest; no network/token is
// required before the first operator setup page becomes available.
func (c *Controller) initialize(ctx context.Context) error {
	var saved baseline
	baselinePath := c.path("baseline-" + c.Config.Seed + ".json")
	if readJSON(baselinePath, &saved) == nil && saved.Compose != nil {
		return nil
	}
	_, hostState, hostInstall, err := c.self(ctx)
	if err != nil {
		return err
	}
	name := "seed-" + c.Config.Seed + "-" + time.Now().UTC().Format("150405.000000000")
	directory := c.path(name)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	archive, err := os.CreateTemp(c.root, ".installed-source-*.tar")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	_, gitErr := c.command(ctx, "git", []string{"-c", "safe.directory=" + installRoot, "-C", installRoot, "archive", c.Config.Revision}, commandOptions{Stdout: archive})
	if gitErr == nil {
		_, err = archive.Seek(0, io.SeekStart)
		if err == nil {
			err = extractTar(archive, directory, false)
		}
	} else {
		err = copyExportedSource(installRoot, directory)
	}
	archive.Close()
	if err != nil {
		return err
	}
	configuration, err := c.render(ctx, directory, c.Config.Revision, hostState+"/"+name, hostState, hostInstall)
	if err != nil {
		return err
	}
	return writeJSON(baselinePath, baseline{Source: name, Compose: configuration})
}

func copyExportedSource(source, destination string) error {
	var manifest struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if readJSON(filepath.Join(source, "RELEASE_MANIFEST.json"), &manifest) != nil || len(manifest.Files) == 0 {
		return errors.New("cannot freeze installed source: local Git objects and release file manifest are unavailable")
	}
	names := []string{"RELEASE_MANIFEST.json"}
	for _, entry := range manifest.Files {
		names = append(names, entry.Path)
	}
	for _, name := range names {
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.Contains(name, `\`) {
			return errors.New("unsafe exported source path")
		}
		input := filepath.Join(source, clean)
		info, err := os.Lstat(input)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("export contains a non-regular source file")
		}
		output := filepath.Join(destination, clean)
		if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		from, err := os.Open(input)
		if err != nil {
			return err
		}
		to, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			from.Close()
			return err
		}
		_, err = io.Copy(to, from)
		from.Close()
		closeErr := to.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
