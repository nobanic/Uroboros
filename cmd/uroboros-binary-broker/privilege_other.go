//go:build !linux

package main

func requireNonRoot() error { return nil }
