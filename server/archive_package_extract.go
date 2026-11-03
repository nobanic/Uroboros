package server

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// archiveEntryBudget is local to one extraction. Admission advances in header
// order before path/type handling, retaining the existing count/byte ceilings.
type archiveEntryBudget struct {
	destination string
	entries     int
	bytes       int64
}

func (budget *archiveEntryBudget) target(header *tar.Header) (string, error) {
	budget.entries++
	if budget.entries > maxArchiveFiles || header.Size < 0 || budget.bytes+header.Size > maxArchiveBytes {
		return "", errors.New("task archive exceeds extraction safety limits")
	}
	budget.bytes += header.Size
	clean := filepath.Clean(filepath.FromSlash(header.Name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe task archive path %q", header.Name)
	}
	target := filepath.Join(budget.destination, clean)
	if relative, err := filepath.Rel(budget.destination, target); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe task archive target %q", header.Name)
	}
	return target, nil
}

func verifyArchivePackage(file *os.File, expected string) error {
	if expected == "" {
		return nil
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(expected) {
		return errors.New("task archive checksum mismatch")
	}
	_, err := file.Seek(0, io.SeekStart)
	return err
}

func extractArchivePackage(path, expectedSHA, destination string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := verifyArchivePackage(file, expectedSHA); err != nil {
		return err
	}
	decoder, err := zstd.NewReader(file)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	if err := os.MkdirAll(destination, archiveDirMode); err != nil {
		return err
	}
	budget := archiveEntryBudget{destination: destination}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := budget.target(header)
		if err != nil {
			return err
		}
		if err := extractArchiveEntry(target, header, reader); err != nil {
			return err
		}
	}
}

func extractArchiveEntry(target string, header *tar.Header, reader io.Reader) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, archiveDirMode)
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(target), archiveDirMode); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode)&0o777)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, reader, header.Size)
		return errors.Join(copyErr, file.Close())
	default:
		return fmt.Errorf("unsupported task archive entry type %d", header.Typeflag)
	}
}
