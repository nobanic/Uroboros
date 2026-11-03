package binaryartifact

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nobanic/UroborOS/binarybroker"
)

const (
	ReferencePending = "pending"
	ReferenceActive  = "active"
	ReferenceMissing = "missing"

	ReferenceOwnerHot  = "hot"
	ReferenceOwnerCold = "cold"
)

// BrokerReference is PostgreSQL's desired state. ReceiptID is populated only
// for a newly staged hot reference; archive/restore transitions are cloned from
// an already durable reference with the same blob hash.
type BrokerReference struct {
	ReferenceID string
	ReceiptID   string
	BlobSHA256  string
	State       string
	OwnerKind   string
	TaskID      int64
	ArchiveID   int64
	ArtifactID  string
	UpdatedAt   time.Time
}

type ReferenceRepository interface {
	ListBinaryBrokerReferences(context.Context) ([]BrokerReference, error)
	SetBinaryBrokerReferenceState(context.Context, string, string, string) error
}

type BrokerClient interface {
	Commit(context.Context, string, string) (binarybroker.Reference, error)
	Bind(context.Context, string, string) (binarybroker.Reference, error)
	Release(context.Context, string) error
	Inventory(context.Context) (binarybroker.Inventory, error)
	Reconcile(context.Context) (binarybroker.ReconcileReport, error)
}

type ReferenceReconcileReport struct {
	Activated int
	Bound     int
	Released  int
	Missing   int
	BrokerGC  binarybroker.ReconcileReport
}

// ReconcileReferences converges the broker to PostgreSQL desired state. It
// always adds/validates every desired destination before releasing any extra
// source. If even one destination cannot be recovered, no source is removed.
func ReconcileReferences(ctx context.Context, repository ReferenceRepository, broker BrokerClient) (ReferenceReconcileReport, error) {
	var report ReferenceReconcileReport
	if repository == nil || broker == nil {
		return report, errors.New("binary reference reconciler is not configured")
	}
	desired, err := repository.ListBinaryBrokerReferences(ctx)
	if err != nil {
		return report, err
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i].ReferenceID < desired[j].ReferenceID })
	desiredByID := make(map[string]BrokerReference, len(desired))
	for _, reference := range desired {
		if reference.ReferenceID == "" || reference.BlobSHA256 == "" {
			return report, errors.New("database returned an incomplete binary broker reference")
		}
		if _, duplicate := desiredByID[reference.ReferenceID]; duplicate {
			return report, fmt.Errorf("database returned duplicate desired reference %s", reference.ReferenceID)
		}
		desiredByID[reference.ReferenceID] = reference
	}
	inventory, err := broker.Inventory(ctx)
	if err != nil {
		return report, err
	}
	actual := make(map[string]binarybroker.Reference, len(inventory.References))
	for _, reference := range inventory.References {
		if previous, duplicate := actual[reference.ID]; duplicate && previous.SHA256 != reference.SHA256 {
			return report, fmt.Errorf("broker returned conflicting reference %s", reference.ID)
		}
		actual[reference.ID] = reference
	}

	var unresolved []error
	for _, wanted := range desired {
		if existing, ok := actual[wanted.ReferenceID]; ok {
			if existing.SHA256 != wanted.BlobSHA256 {
				unresolved = append(unresolved, fmt.Errorf("broker reference %s has hash %s, database requires %s", wanted.ReferenceID, existing.SHA256, wanted.BlobSHA256))
				_ = repository.SetBinaryBrokerReferenceState(ctx, wanted.ReferenceID, ReferenceMissing, "broker hash mismatch")
				report.Missing++
				continue
			}
			if err := repository.SetBinaryBrokerReferenceState(ctx, wanted.ReferenceID, ReferenceActive, ""); err != nil {
				return report, err
			}
			if wanted.State != ReferenceActive {
				report.Activated++
			}
			continue
		}
		var created binarybroker.Reference
		if wanted.ReceiptID != "" {
			created, err = broker.Commit(ctx, wanted.ReceiptID, wanted.ReferenceID)
		} else {
			sourceID := findReferenceWithHash(actual, wanted.BlobSHA256)
			if sourceID == "" {
				err = errors.New("no receipt or same-hash source reference exists")
			} else {
				created, err = broker.Bind(ctx, sourceID, wanted.ReferenceID)
				if err == nil {
					report.Bound++
				}
			}
		}
		if err != nil || created.ID != wanted.ReferenceID || created.SHA256 != wanted.BlobSHA256 {
			message := "broker could not materialize desired reference"
			if err != nil {
				message = err.Error()
			} else {
				err = errors.New("broker materialized the wrong reference identity or blob hash")
			}
			_ = repository.SetBinaryBrokerReferenceState(ctx, wanted.ReferenceID, ReferenceMissing, message)
			report.Missing++
			unresolved = append(unresolved, fmt.Errorf("reference %s: %w", wanted.ReferenceID, err))
			continue
		}
		actual[created.ID] = created
		if err := repository.SetBinaryBrokerReferenceState(ctx, wanted.ReferenceID, ReferenceActive, ""); err != nil {
			return report, err
		}
		report.Activated++
	}
	if len(unresolved) > 0 {
		return report, errors.Join(unresolved...)
	}

	extra := make([]string, 0)
	for id := range actual {
		if _, keep := desiredByID[id]; !keep {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		if err := broker.Release(ctx, id); err != nil {
			return report, fmt.Errorf("release obsolete broker reference %s: %w", id, err)
		}
		report.Released++
	}
	report.BrokerGC, err = broker.Reconcile(ctx)
	return report, err
}

func findReferenceWithHash(actual map[string]binarybroker.Reference, hash string) string {
	ids := make([]string, 0)
	for id, reference := range actual {
		if reference.SHA256 == hash {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}
