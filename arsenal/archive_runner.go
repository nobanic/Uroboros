package arsenal

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/nobanic/UroborOS/db"
)

const archiveMaxDeferredRetry = 10 * time.Minute

type archiveRunStats struct {
	Provider     string `json:"provider"`
	Contract     string `json:"contract_version"`
	Pages        int    `json:"pages"`
	Rows         int    `json:"rows"`
	Attempts     int    `json:"attempts"`
	Retries      int    `json:"retries"`
	RawRetained  bool   `json:"raw_retained"`
	Complete     bool   `json:"complete"`
	LastCursorID string `json:"last_continuation_sha256,omitempty"`
}

func (m *Manager) acquireArchiveProvider(ctx context.Context) (func(), error) {
	select {
	case m.archiveGate <- struct{}{}:
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	release := func() { <-m.archiveGate }
	m.archiveScheduleMu.Lock()
	wait := archiveCourtesyInterval - m.archiveNow().Sub(m.archiveLastStart)
	m.archiveScheduleMu.Unlock()
	if wait > 0 {
		if err := m.archiveSleep(ctx, wait); err != nil {
			release()
			return nil, err
		}
	}
	m.archiveScheduleMu.Lock()
	m.archiveLastStart = m.archiveNow()
	m.archiveScheduleMu.Unlock()
	return release, nil
}

func archiveConfigDates(config map[string]any) (string, string) {
	from, _ := config["from"].(string)
	to, _ := config["to"].(string)
	return from, to
}

func (m *Manager) reauthorizeArchive(ctx context.Context, job *db.ArsenalJob, prepared preparedRun) error {
	if prepared.archiveGrant == nil {
		return errors.New("archive provider grant is unavailable")
	}
	from, to := archiveConfigDates(prepared.config)
	grant, err := m.pg.AuthorizeArchiveProviderRequest(
		ctx, job.ID, prepared.archiveGrant.ID, prepared.target, from, to, m.archiveNow().UTC(),
	)
	if err != nil {
		if errors.Is(err, db.ErrArchiveAdmission) {
			return errors.New("archive provider grant or task admission is no longer active")
		}
		return err
	}
	if grant.ContractVersion != ArchiveContractVersion || grant.NoticeVersion != ArchiveNoticeVersion {
		return errors.New("archive provider grant uses an unsupported contract or notice revision")
	}
	return nil
}

func archiveAttemptProjection(outcome, errorCode string) map[string]any {
	return map[string]any{
		"provider": db.ArchiveProviderInternetArchive, "contract_version": ArchiveContractVersion,
		"projection_version": archiveProjectionVersion, "raw_retained": false,
		"rows": []any{}, "outcome": outcome, "error_code": errorCode,
	}
}

func archiveProjectionHash(projection map[string]any) (string, error) {
	raw, err := canonicalJSON(projection)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

func (m *Manager) beginArchiveAttempt(
	ctx context.Context,
	job *db.ArsenalJob,
	prepared preparedRun,
	attemptID, requestSHA string,
	response archiveFetchResponse,
) error {
	projection := archiveAttemptProjection("started", "")
	projectionSHA, err := archiveProjectionHash(projection)
	if err != nil {
		return err
	}
	started := response.StartedAt
	if started.IsZero() {
		started = m.archiveNow()
	}
	_, err = m.pg.BeginArchiveProviderAttempt(ctx, db.ArchiveProviderArtifactCreate{
		JobID: job.ID, TaskID: job.TaskID, GrantID: prepared.archiveGrant.ID,
		Provider: db.ArchiveProviderInternetArchive, AttemptID: attemptID,
		RequestSHA256: requestSHA, RouteSHA256: response.RouteSHA256,
		ProjectionSHA256: projectionSHA, Projection: projection,
		Outcome: "started", StartedAt: started, FinishedAt: started,
	})
	return err
}

func (m *Manager) completeArchiveAttempt(
	ctx context.Context,
	job *db.ArsenalJob,
	prepared preparedRun,
	attemptID, requestSHA string,
	response archiveFetchResponse,
	outcome, errorCode string,
	projection map[string]any,
	observations []db.ArchiveObservationCreate,
) error {
	if projection == nil {
		projection = archiveAttemptProjection(outcome, errorCode)
	}
	projectionSHA, err := archiveProjectionHash(projection)
	if err != nil {
		return err
	}
	rawSHA := ""
	if response.Body != nil {
		rawSHA = sha256Hex(response.Body)
	}
	routeSHA := response.RouteSHA256
	if routeSHA == "" {
		routeSHA = sha256Hex([]byte("unresolved"))
	}
	started := response.StartedAt
	finished := response.FinishedAt
	if started.IsZero() {
		started = m.archiveNow()
	}
	if finished.IsZero() || finished.Before(started) {
		finished = started
	}
	var status *int
	if response.Status > 0 {
		value := response.Status
		status = &value
	}
	_, err = m.pg.CompleteArchiveProviderAttempt(ctx, db.ArchiveProviderArtifactCreate{
		JobID: job.ID, TaskID: job.TaskID, GrantID: prepared.archiveGrant.ID,
		Provider: db.ArchiveProviderInternetArchive, AttemptID: attemptID,
		RequestSHA256: requestSHA, RouteSHA256: routeSHA, ResponseStatus: status,
		RawBodySHA256: rawSHA, ProjectionSHA256: projectionSHA, Projection: projection,
		Outcome: outcome, ErrorCode: errorCode, StartedAt: started, FinishedAt: finished,
	}, observations)
	return err
}

func archivePersistenceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (m *Manager) executeArchive(parent context.Context, job *db.ArsenalJob, prepared preparedRun) {
	finish := db.ArsenalJobFinish{Status: "failed", ExitState: "provider_error"}
	stats := &archiveRunStats{
		Provider: db.ArchiveProviderInternetArchive, Contract: ArchiveContractVersion,
		RawRetained: false,
	}
	finish.Stats = stats
	defer func() {
		if err := m.pg.FinishArsenalJob(context.Background(), job.ID, finish); err != nil {
			// Finish errors contain only host-owned identifiers and generic lifecycle state.
			log.Printf("[arsenal] archive job %d finalization failed: %v", job.ID, err)
			return
		}
		if _, err := m.pg.ReconcileArchiveProviderAttempts(context.Background()); err != nil {
			log.Printf("[arsenal] archive job %d attempt reconciliation failed: %v", job.ID, err)
		}
	}()
	if prepared.archiveGrant == nil || len(prepared.archiveGrant.IdentityKey) < 32 {
		finish.ExitState = "contract_error"
		finish.Error = "archive provider grant identity is unavailable"
		return
	}
	if prepared.archiveGrant.ContractVersion != ArchiveContractVersion ||
		prepared.archiveGrant.NoticeVersion != ArchiveNoticeVersion {
		finish.ExitState = "contract_error"
		finish.Error = "archive provider grant uses an unsupported contract or notice revision"
		return
	}
	if err := m.pg.MarkArsenalJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	if err := m.reauthorizeArchive(parent, job, prepared); err != nil {
		finish.ExitState = "not_admitted"
		finish.Error = err.Error()
		return
	}

	from, to := archiveConfigDates(prepared.config)
	cursor := ""
	seenCursors := map[string]bool{}
	for pageNumber := 1; ; pageNumber++ {
		pageCommitted := false
		for attempt := 0; attempt <= archiveMaxTransientRetries; attempt++ {
			if cause := context.Cause(parent); cause != nil {
				finish.Status = "canceled"
				finish.ExitState = "canceled"
				finish.Error = "archive discovery canceled"
				return
			}
			request := archiveFetchRequest{Domain: prepared.target, CaptureFrom: from, CaptureTo: to, Cursor: cursor}
			_, requestSHA, err := compileArchiveProviderURL(request)
			if err != nil {
				finish.ExitState = "contract_error"
				finish.Error = err.Error()
				return
			}
			attemptID := fmt.Sprintf("%d-%d-%d", job.ID, pageNumber, attempt+1)
			release, err := m.acquireArchiveProvider(parent)
			if err != nil {
				finish.Status = "canceled"
				finish.ExitState = "canceled"
				finish.Error = "archive discovery canceled"
				return
			}
			if err := m.reauthorizeArchive(parent, job, prepared); err != nil {
				release()
				finish.ExitState = "not_admitted"
				finish.Error = err.Error()
				return
			}
			attemptBegan := false
			response, fetchErr := m.archiveFetch(parent, request, func(admission archiveFetchResponse) error {
				if err := m.beginArchiveAttempt(parent, job, prepared, attemptID, requestSHA, admission); err != nil {
					return err
				}
				attemptBegan = true
				return nil
			})
			release()
			if attemptBegan {
				stats.Attempts++
			}
			if cause := context.Cause(parent); cause != nil {
				if attemptBegan {
					persistCtx, persistCancel := archivePersistenceContext()
					storeErr := m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
						"network_error", "canceled", nil, nil)
					persistCancel()
					if storeErr != nil {
						finish.Status = "failed"
						finish.ExitState = "recording_error"
						finish.Error = "store canceled archive provider attempt: " + storeErr.Error()
						return
					}
				}
				finish.Status = "canceled"
				finish.ExitState = "canceled"
				finish.Error = "archive discovery canceled"
				return
			}
			if fetchErr != nil {
				var admissionErr archiveAdmissionRecordError
				if errors.As(fetchErr, &admissionErr) {
					finish.ExitState = "recording_error"
					finish.Error = "archive provider request was blocked because its admission could not be recorded"
					return
				}
			}
			if fetchErr != nil {
				code := "network_failure"
				outcome := "network_error"
				if errors.Is(fetchErr, errArchiveRedirect) {
					code, outcome = "redirect_rejected", "contract_error"
				} else if errors.Is(fetchErr, errArchiveBodyLimit) {
					code, outcome = "body_limit", "contract_error"
				}
				if attemptBegan {
					persistCtx, persistCancel := archivePersistenceContext()
					err = m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
						outcome, code, nil, nil)
					persistCancel()
					if err != nil {
						finish.ExitState = "recording_error"
						finish.Error = "store archive provider attempt: " + err.Error()
						return
					}
				}
				if outcome == "contract_error" {
					finish.ExitState = "contract_error"
					if code == "body_limit" {
						finish.Error = "archive provider response exceeded the decoded body limit"
					} else {
						finish.Error = "archive provider redirect rejected"
					}
					return
				}
				if attempt == archiveMaxTransientRetries {
					finish.ExitState = "network_error"
					finish.Error = "archive provider request failed after bounded retries"
					return
				}
				stats.Retries++
				continue
			}

			if response.Status == http.StatusTooManyRequests || response.Status >= 500 {
				outcome := "http_error"
				if response.Status == http.StatusTooManyRequests {
					outcome = "rate_limited"
				}
				code := fmt.Sprintf("http_%d", response.Status)
				persistCtx, persistCancel := archivePersistenceContext()
				err := m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
					outcome, code, nil, nil)
				persistCancel()
				if err != nil {
					finish.ExitState = "recording_error"
					finish.Error = "store archive provider attempt: " + err.Error()
					return
				}
				if attempt == archiveMaxTransientRetries {
					finish.ExitState = map[bool]string{true: "rate_limited", false: "provider_error"}[response.Status == http.StatusTooManyRequests]
					finish.Error = "archive provider transient failure exceeded bounded retries"
					return
				}
				delay := archiveCourtesyInterval
				if parsed, ok := retryAfterDuration(response.RetryAfter, m.archiveNow()); ok {
					delay = parsed
				}
				if delay > archiveMaxDeferredRetry {
					finish.ExitState = "rate_limited"
					finish.Error = "archive provider requested a retry beyond the local deferred-retry window"
					return
				}
				if deadline, ok := parent.Deadline(); ok && m.archiveNow().Add(delay).After(deadline) {
					finish.ExitState = "rate_limited"
					finish.Error = "archive provider retry does not fit the remaining task deadline"
					return
				}
				stats.Retries++
				if err := m.archiveSleep(parent, delay); err != nil {
					finish.Status = "canceled"
					finish.ExitState = "canceled"
					finish.Error = "archive discovery canceled"
					return
				}
				continue
			}

			if response.Status != http.StatusOK {
				code := fmt.Sprintf("http_%d", response.Status)
				persistCtx, persistCancel := archivePersistenceContext()
				err := m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
					"http_error", code, nil, nil)
				persistCancel()
				if err != nil {
					finish.ExitState = "recording_error"
					finish.Error = "store archive provider attempt: " + err.Error()
					return
				}
				finish.ExitState = "provider_error"
				if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
					finish.Error = "archive provider requires authentication; anonymous archive discovery stopped"
				} else if response.Status >= 300 && response.Status < 400 {
					finish.ExitState = "contract_error"
					finish.Error = "archive provider redirect rejected"
				} else {
					finish.Error = "archive provider returned an unexpected HTTP status"
				}
				return
			}
			if !archiveResponseContentType(response.ContentType) {
				persistCtx, persistCancel := archivePersistenceContext()
				err := m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
					"contract_error", "content_type", nil, nil)
				persistCancel()
				if err != nil {
					finish.ExitState = "recording_error"
					finish.Error = "store archive provider attempt: " + err.Error()
					return
				}
				finish.ExitState = "contract_error"
				finish.Error = "archive provider response is not the pinned JSON contract"
				return
			}
			rawSHA := sha256Hex(response.Body)
			page, err := parseArchivePage(response.Body, prepared.target, prepared.archiveGrant.IdentityKey,
				attemptID, rawSHA, prepared.scopeHash, prepared.configHash, response.FinishedAt)
			if err != nil {
				persistCtx, persistCancel := archivePersistenceContext()
				storeErr := m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
					"contract_error", "schema", nil, nil)
				persistCancel()
				if storeErr != nil {
					finish.ExitState = "recording_error"
					finish.Error = "store archive provider attempt: " + storeErr.Error()
					return
				}
				finish.ExitState = "contract_error"
				finish.Error = err.Error()
				return
			}
			persistCtx, persistCancel := archivePersistenceContext()
			err = m.completeArchiveAttempt(persistCtx, job, prepared, attemptID, requestSHA, response,
				"page", "", page.Projection, page.Observations)
			persistCancel()
			if err != nil {
				finish.ExitState = "recording_error"
				finish.Error = "store archive provider page: " + err.Error()
				return
			}
			stats.Pages++
			stats.Rows += page.Rows
			pageCommitted = true
			if page.Cursor == "" {
				stats.Complete = true
				finish.Status = "completed"
				finish.Complete = true
				finish.ExitState = "ok"
				if stats.Rows == 0 {
					finish.ExitState = "ok_empty"
				}
				finish.Error = ""
				return
			}
			cursorID := optionalSHA256(page.Cursor)
			stats.LastCursorID = cursorID
			if seenCursors[cursorID] {
				finish.ExitState = "partial"
				finish.Error = "archive provider repeated a continuation key"
				return
			}
			seenCursors[cursorID] = true
			cursor = page.Cursor
			break
		}
		if !pageCommitted {
			finish.ExitState = "partial"
			finish.Error = "archive provider page was not committed"
			return
		}
	}
}
