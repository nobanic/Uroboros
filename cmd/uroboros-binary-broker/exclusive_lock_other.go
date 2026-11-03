//go:build !linux

package main

import "errors"

type brokerProcessLock struct{}

func acquireBrokerProcessLock(string) (*brokerProcessLock, error) {
	return nil, errors.New("binary artifact broker process locking is supported only on Linux")
}

func (*brokerProcessLock) Close() error { return nil }
