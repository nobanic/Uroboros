package server

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
	pgdb "github.com/nobanic/UroborOS/db"
)

// archivePackageOutput owns the temporary output until publication. In
// particular, chmod failure after rename retains the already-published path,
// matching the original recovery boundary; abort only removes the partial file.
type archivePackageOutput struct {
	path, partial string
	file          *os.File
	digest        hash.Hash
	published     bool
}

func openArchivePackageOutput(path string) (*archivePackageOutput, error) {
	partial := path + ".partial"
	_ = os.Remove(partial)
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, archiveFileMode)
	if err != nil {
		return nil, err
	}
	return &archivePackageOutput{path: path, partial: partial, file: file, digest: sha256.New()}, nil
}

func (output *archivePackageOutput) release() {
	_ = output.file.Close()
	if !output.published {
		_ = os.Remove(output.partial)
	}
}

func (output *archivePackageOutput) publish() (int64, string, error) {
	if err := output.file.Sync(); err != nil {
		return 0, "", err
	}
	if err := output.file.Close(); err != nil {
		return 0, "", err
	}
	info, err := os.Stat(output.partial)
	if err != nil {
		return 0, "", err
	}
	size, checksum := info.Size(), hex.EncodeToString(output.digest.Sum(nil))
	if err := os.Rename(output.partial, output.path); err != nil {
		return 0, "", err
	}
	if err := os.Chmod(output.path, archiveFileMode); err != nil {
		return 0, "", err
	}
	output.published = true
	return size, checksum, nil
}

func persistArchiveManifest(payloadDir string, snapshot *pgdb.TaskArchiveSnapshot) error {
	file, err := os.OpenFile(filepath.Join(payloadDir, "manifest.json"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, archiveFileMode)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(snapshot)
	var syncErr error
	if encodeErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(encodeErr, syncErr, file.Close())
}

func buildArchivePackage(path, payloadDir string, snapshot *pgdb.TaskArchiveSnapshot) (int64, int64, string, error) {
	if snapshot == nil {
		return 0, 0, "", errors.New("nil task archive snapshot")
	}
	if err := os.MkdirAll(filepath.Dir(path), archiveDirMode); err != nil {
		return 0, 0, "", err
	}
	if err := persistArchiveManifest(payloadDir, snapshot); err != nil {
		return 0, 0, "", err
	}
	output, err := openArchivePackageOutput(path)
	if err != nil {
		return 0, 0, "", err
	}
	defer output.release()
	multi := io.MultiWriter(output.file, output.digest)
	encoder, err := zstd.NewWriter(multi,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(10)),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		return 0, 0, "", err
	}
	tarWriter := tar.NewWriter(encoder)
	original, walkErr := writeArchivePayload(payloadDir, tarWriter)
	if walkErr != nil {
		_ = tarWriter.Close()
		_ = encoder.Close()
		return 0, 0, "", walkErr
	}
	if err := tarWriter.Close(); err != nil {
		_ = encoder.Close()
		return 0, 0, "", err
	}
	if err := encoder.Close(); err != nil {
		return 0, 0, "", err
	}
	size, checksum, err := output.publish()
	if err != nil {
		return 0, 0, "", err
	}
	return original, size, checksum, nil
}

// writeArchivePayload is installed from the original WalkDir callback by the
// operator edit. Its traversal/header/symlink/copy algorithm is AST-protected.

func writeArchivePayload(payloadDir string, tarWriter *tar.Writer) (int64, error) {
	var originalSize int64
	walkErr := filepath.WalkDir(payloadDir, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(payloadDir, current)
		if err != nil || relative == "." {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// End to end, the archive format only supports regular files and directories (the extractor errors out on any other type),
			// so symlinks cannot be restored. Skip rather than fail the whole package: the link target is not read (lstat, stays within the directory tree),
			// and no symlink entry is written; when a link points inside the tree, the target file itself is still walked and archived separately.
			log.Printf("[task-archive] skipping symlink (unsupported by archive, does not affect other files): %s", current)
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		originalSize += info.Size()
		input, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		return errors.Join(copyErr, closeErr)
	})
	return originalSize, walkErr
}
