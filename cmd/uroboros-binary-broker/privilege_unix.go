//go:build linux

package main

import (
	"errors"
	"os"
)

func requireNonRoot() error {
	if os.Geteuid() == 0 {
		return errors.New("binary artifact broker refuses to run as root")
	}
	return nil
}
