//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type brokerProcessLock struct {
	file *os.File
}

func acquireBrokerProcessLock(root string) (*brokerProcessLock, error) {
	path := filepath.Join(root, "broker.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open broker process lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = file.Close()
		}
	}()
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.New("another binary artifact broker owns this CAS")
		}
		return nil, fmt.Errorf("lock broker CAS: %w", err)
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return nil, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("broker process lock must be a regular file owned by the broker uid")
	}
	if err := syscall.Fchmod(fd, 0o600); err != nil {
		return nil, err
	}
	closeOnError = false
	return &brokerProcessLock{file: file}, nil
}

func (lock *brokerProcessLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}
