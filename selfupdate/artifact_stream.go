package selfupdate

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"time"
)

// artifactStream centralizes copy/check/flush ordering. An archive's extracted
// size is checked before flushing; download completeness is checked afterward.
// The caller owns file/body lifetime and retains its original open permissions.
type artifactStream struct {
	output                    *os.File
	observers                 []io.Writer
	beforeFlush, afterFlush   func(int64) error
	copyFailure, flushFailure func(error) error
}

func (stream artifactStream) transfer(input io.Reader) error {
	var writer io.Writer = stream.output
	if len(stream.observers) != 0 {
		writers := append([]io.Writer{writer}, stream.observers...)
		writer = io.MultiWriter(writers...)
	}
	count, failure := io.Copy(writer, input)
	if failure != nil {
		return stream.copyFailure(failure)
	}
	if stream.beforeFlush != nil {
		if failure = stream.beforeFlush(count); failure != nil {
			return failure
		}
	}
	if failure = stream.output.Sync(); failure != nil {
		if stream.flushFailure != nil {
			return stream.flushFailure(failure)
		}
		return failure
	}
	if stream.afterFlush != nil {
		return stream.afterFlush(count)
	}
	return nil
}

func (source releaseSource) download(asset Asset, destination string, progress Progress) (string, error) {
	reader, failure := source.body(asset.URL)
	if failure != nil {
		return "", fmt.Errorf("download %s: %w", asset.Name, failure)
	}
	defer reader.Close()
	output, failure := os.Create(destination)
	if failure != nil {
		return "", fmt.Errorf("create temporary file: %w", failure)
	}
	defer output.Close()
	digest := sha256.New()
	meter := &progressWriter{total: asset.Size, prog: progress, name: asset.Name, last: time.Now()}
	stream := artifactStream{
		output: output, observers: []io.Writer{digest, meter},
		copyFailure:  func(cause error) error { return fmt.Errorf("download interrupted: %w", cause) },
		flushFailure: func(cause error) error { return fmt.Errorf("flush to disk failed: %w", cause) },
		afterFlush: func(int64) error {
			if asset.Size > 0 && meter.written != asset.Size {
				return fmt.Errorf("incomplete download: expected %d bytes, got %d bytes", asset.Size, meter.written)
			}
			return nil
		},
	}
	if failure = stream.transfer(reader); failure != nil {
		return "", failure
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func extractUpdateArtifact(archivePath, destination string) error {
	name := "uroboros"
	if runtime.GOOS == "windows" {
		name = "uroboros.exe"
	}
	archive, failure := zip.OpenReader(archivePath)
	if failure != nil {
		return fmt.Errorf("open the release package: %w", failure)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), name) {
			continue
		}
		reader, failure := entry.Open()
		if failure != nil {
			return fmt.Errorf("read %s: %w", entry.Name, failure)
		}
		defer reader.Close()
		output, failure := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if failure != nil {
			return fmt.Errorf("write out the new binary: %w", failure)
		}
		defer output.Close()
		stream := artifactStream{
			output:      output,
			copyFailure: func(cause error) error { return fmt.Errorf("extract %s: %w", entry.Name, cause) },
			beforeFlush: func(count int64) error {
				if count > maxBinarySize {
					return fmt.Errorf("the executable in the release package exceeds %s, refusing to extract", humanSize(maxBinarySize))
				}
				if count == 0 {
					return fmt.Errorf("the %s in the release package is an empty file", name)
				}
				return nil
			},
		}
		return stream.transfer(io.LimitReader(reader, maxBinarySize+1))
	}
	return fmt.Errorf("could not find %s in the release package", name)
}
