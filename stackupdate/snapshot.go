package stackupdate

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

type volumeReceipt struct {
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
}

// VolumeSnapshot runs in a short-lived, network-isolated container with only
// the selected installation's volume mounts. Restore verifies every archive
// before removing any contents. It never removes Docker volumes themselves.
func VolumeSnapshot(restore bool, backupRoot, volumesRoot string) error {
	var receipts []volumeReceipt
	validRole := regexp.MustCompile(`^[a-z][a-z0-9_]{0,70}$`)
	if restore {
		if err := readJSON(filepath.Join(backupRoot, "volumes.json"), &receipts); err != nil {
			return err
		}
		if len(receipts) == 0 {
			return errors.New("empty volume backup manifest")
		}
		for _, receipt := range receipts {
			if !validRole.MatchString(receipt.Role) {
				return errors.New("invalid volume role in backup")
			}
			hash, err := fileHash(filepath.Join(backupRoot, receipt.Role+".tar.gz"))
			if err != nil || hash != receipt.SHA256 {
				return fmt.Errorf("volume backup checksum mismatch: %s", receipt.Role)
			}
			if info, err := os.Lstat(filepath.Join(volumesRoot, receipt.Role)); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("restore target is not a mounted volume directory")
			}
		}
		for _, receipt := range receipts {
			root := filepath.Join(volumesRoot, receipt.Role)
			entries, err := os.ReadDir(root)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
			f, err := os.Open(filepath.Join(backupRoot, receipt.Role+".tar.gz"))
			if err != nil {
				return err
			}
			reader, err := gzip.NewReader(f)
			if err != nil {
				f.Close()
				return err
			}
			err = extractTar(reader, root, true)
			reader.Close()
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	entries, err := os.ReadDir(volumesRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validRole.MatchString(entry.Name()) {
			return errors.New("invalid snapshot volume mount")
		}
		root := filepath.Join(volumesRoot, entry.Name())
		path := filepath.Join(backupRoot, entry.Name()+".tar.gz")
		if err := archiveVolume(root, path); err != nil {
			return err
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		receipts = append(receipts, volumeReceipt{Role: entry.Name(), SHA256: hash})
	}
	if len(receipts) == 0 {
		return errors.New("no volume mounts to snapshot")
	}
	if err := writeJSON(filepath.Join(backupRoot, "volumes.json"), receipts); err != nil {
		return err
	}
	return matchDirectoryOwner(filepath.Join(backupRoot, "volumes.json"), backupRoot)
}

func archiveVolume(root, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(f)
	archive := tar.NewWriter(compressed)
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root || info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return errors.New("volume contains a file type that cannot be restored safely")
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(archive, file)
		return err
	})
	for _, close := range []func() error{archive.Close, compressed.Close, f.Close} {
		if closeErr := close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = matchDirectoryOwner(path, filepath.Dir(path))
	}
	return err
}

func matchDirectoryOwner(path, directory string) error {
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	return os.Chown(path, header.Uid, header.Gid)
}
