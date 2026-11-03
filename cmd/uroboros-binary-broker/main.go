package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nobanic/UroborOS/binarybroker"
)

const (
	rootEnvironment         = "UROBOROS_BINARY_BROKER_ROOT"
	socketEnvironment       = "UROBOROS_BINARY_BROKER_SOCKET"
	workerSocketEnvironment = "UROBOROS_BINARY_WORKER_SOCKET"
	workerImageEnvironment  = "UROBOROS_BINARY_WORKER_IMAGE_DIGEST"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if runtime.GOOS != "linux" {
		return errors.New("binary artifact broker runtime is supported only on Linux")
	}
	if err := requireNonRoot(); err != nil {
		return err
	}
	root := filepath.Clean(strings.TrimSpace(os.Getenv(rootEnvironment)))
	socketPath := filepath.Clean(strings.TrimSpace(os.Getenv(socketEnvironment)))
	workerSocketPath := filepath.Clean(strings.TrimSpace(os.Getenv(workerSocketEnvironment)))
	workerImageDigest := strings.TrimSpace(os.Getenv(workerImageEnvironment))
	if root == "." || socketPath == "." || workerSocketPath == "." || !filepath.IsAbs(root) ||
		!filepath.IsAbs(socketPath) || !filepath.IsAbs(workerSocketPath) {
		return fmt.Errorf("%s, %s and %s must be absolute paths", rootEnvironment, socketEnvironment, workerSocketEnvironment)
	}
	socketDir := filepath.Dir(socketPath)
	workerSocketDir := filepath.Dir(workerSocketPath)
	if pathsOverlap(root, socketDir) || pathsOverlap(root, workerSocketDir) || pathsOverlap(socketDir, workerSocketDir) {
		return errors.New("broker CAS, application bootstrap socket, and worker socket must be disjoint mounts")
	}
	if err := prepareSocketDirectory(socketDir); err != nil {
		return err
	}
	if err := prepareWorkerSocketDirectory(workerSocketDir); err != nil {
		return err
	}
	store, err := binarybroker.NewStore(binarybroker.Config{Root: root})
	if err != nil {
		return err
	}
	processLock, err := acquireBrokerProcessLock(root)
	if err != nil {
		return err
	}
	defer processLock.Close() //nolint:errcheck
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedSocketDir, err := filepath.EvalSymlinks(socketDir)
	if err != nil {
		return err
	}
	if pathsOverlap(resolvedRoot, resolvedSocketDir) {
		return errors.New("resolved broker CAS and bootstrap socket mounts overlap")
	}
	resolvedWorkerSocketDir, err := filepath.EvalSymlinks(workerSocketDir)
	if err != nil {
		return err
	}
	if pathsOverlap(resolvedRoot, resolvedWorkerSocketDir) || pathsOverlap(resolvedSocketDir, resolvedWorkerSocketDir) {
		return errors.New("resolved broker socket mounts overlap")
	}
	for _, path := range []string{socketPath, workerSocketPath} {
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return errors.New("broker socket path already exists and is not a socket")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := store.Reconcile(time.Now().UTC(), binarybroker.MinimumGCGrace); err != nil {
		return fmt.Errorf("startup reconcile: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socketPath) //nolint:errcheck
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return err
	}
	workerListener, err := net.Listen("unix", workerSocketPath)
	if err != nil {
		return err
	}
	defer workerListener.Close()
	defer os.Remove(workerSocketPath) //nolint:errcheck
	if err := os.Chmod(workerSocketPath, 0o620); err != nil {
		return err
	}
	coordinator, err := binarybroker.NewWorkerCoordinator(store, binarybroker.StandardWorkerPolicy(workerImageDigest), 610*time.Second)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan error, 1)
	go func() { workerDone <- coordinator.ServeWorkers(ctx, workerListener) }()
	log.Print("binary artifact broker awaiting its single bootstrap connection")
	server := binarybroker.ProtocolServer{Store: store, GCGrace: binarybroker.MinimumGCGrace, Workers: coordinator}
	serveErr := server.ServeOnceContext(ctx, listener, func() error {
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		log.Print("binary artifact broker bootstrap listener removed")
		return nil
	})
	stop()
	workerErr := <-workerDone
	return errors.Join(serveErr, workerErr)
}

func prepareSocketDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("bootstrap socket directory must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(resolved), filepath.Clean(absolute)) {
		return errors.New("bootstrap socket directory must not traverse symlinks")
	}
	return os.Chmod(path, 0o700)
}

func prepareWorkerSocketDirectory(path string) error {
	if err := prepareSocketDirectory(path); err != nil {
		return err
	}
	// os.FileMode stores special bits outside the low Unix permission bits;
	// numeric 02000 would be ignored by os.Chmod instead of setting setgid.
	if err := os.Chmod(path, 0o710|os.ModeSetgid); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o710 || info.Mode()&os.ModeSetgid == 0 {
		return errors.New("worker socket directory did not retain setgid permissions")
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
		if err != nil {
			return false
		}
		return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return contains(left, right) || contains(right, left)
}
