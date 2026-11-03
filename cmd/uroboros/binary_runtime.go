package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/binaryartifact"
	"github.com/nobanic/UroborOS/binarybroker"
	"github.com/nobanic/UroborOS/binaryintake"
	"github.com/nobanic/UroborOS/server"
)

const (
	binaryBrokerSocketEnvironment = "UROBOROS_BINARY_BROKER_SOCKET"
	binaryWorkerImageEnvironment  = "UROBOROS_BINARY_WORKER_IMAGE_DIGEST"
)

type bootstrappedBinaryRuntime struct {
	client *binarybroker.Client
	option server.Option
}

func bootstrapBinaryRuntime(parent context.Context, manager *server.Manager) (*bootstrappedBinaryRuntime, error) {
	socketPath := strings.TrimSpace(os.Getenv(binaryBrokerSocketEnvironment))
	workerImageDigest := strings.TrimSpace(os.Getenv(binaryWorkerImageEnvironment))
	if socketPath == "" && workerImageDigest == "" {
		return nil, nil
	}
	if socketPath == "" || workerImageDigest == "" {
		return nil, fmt.Errorf("%s and %s must either both be set or both be absent",
			binaryBrokerSocketEnvironment, binaryWorkerImageEnvironment)
	}
	if manager == nil || manager.PG() == nil || manager.Traffic() == nil {
		return nil, errors.New("binary runtime requires PostgreSQL and traffic evidence storage")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	connection, err := dialBinaryBroker(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect one-shot binary broker bootstrap socket: %w", err)
	}
	client, err := binarybroker.NewClient(connection)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = client.Close()
		}
	}()
	if report, err := binaryartifact.ReconcileReferences(ctx, manager.PG(), client); err != nil {
		return nil, fmt.Errorf("startup binary reference reconcile (%+v): %w", report, err)
	}
	interrupted, err := manager.PG().ReconcileBinaryTriageJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile binary triage leases: %w", err)
	}
	policy := binarybroker.StandardWorkerPolicy(workerImageDigest)
	runner, err := binaryintake.NewRunner(manager.PG(), client, 4096, 630*time.Second)
	if err != nil {
		return nil, err
	}
	runner.OnError = func(work binaryintake.Work, err error) {
		log.Printf("[binary-triage] task=%d job=%s artifact=%s failed: %v", work.TaskID, work.JobID, work.ArtifactID, err)
	}
	intake, err := binaryintake.New(manager.Traffic(), manager.PG(), client, binaryintake.Config{
		WorkerImageDigest: policy.ImageDigest, ToolRulePins: policy.ToolRulePins, LimitProfile: policy.LimitProfile,
	}, runner)
	if err != nil {
		return nil, err
	}
	queued, err := manager.PG().ListQueuedBinaryTriageWork(ctx, 4096)
	if err != nil {
		return nil, fmt.Errorf("load queued binary triage: %w", err)
	}
	for _, item := range queued {
		if err := runner.Enqueue(binaryintake.Work{TaskID: item.TaskID, ArtifactID: item.ArtifactID,
			JobID: item.JobID, ReferenceID: item.ReferenceID, Fingerprint: item.Fingerprint}); err != nil {
			return nil, fmt.Errorf("restore queued binary triage %s: %w", item.JobID, err)
		}
	}
	log.Printf("[binary-triage] runtime enabled; restored queued=%d interrupted_leases=%d worker=%s",
		len(queued), interrupted, workerImageDigest)
	closeOnError = false
	return &bootstrappedBinaryRuntime{client: client, option: server.WithBinaryRuntime(intake, runner)}, nil
}

// dialBinaryBroker tolerates container start-order races only before the first
// successful connection. The returned client deliberately has no reconnect
// path once the broker removes its bootstrap listener.
func dialBinaryBroker(ctx context.Context, socketPath string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	var lastErr error
	for {
		connection, err := dialer.DialContext(ctx, "unix", socketPath)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}
