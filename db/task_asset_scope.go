package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
)

// A scope registration has one result accumulator across preflight and each
// rule. Storage is atomic while returned counters retain partial progress.
type taskAssetScopeRegistration struct {
	store  *AssetStore
	taskID int64
	rules  []ParsedScope
	result TaskAssetScopeMutation
}

func (registration *taskAssetScopeRegistration) apply(inputs []ScopeInput) (TaskAssetScopeMutation, error) {
	registration.result = TaskAssetScopeMutation{Requested: len(inputs)}
	if err := registration.prepare(inputs); err != nil {
		return registration.result, err
	}
	tx, err := registration.store.db.Begin()
	if err != nil {
		return registration.result, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(tx); err != nil {
		return registration.result, err
	}
	var taskExists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, registration.taskID).Scan(&taskExists); err != nil {
		return registration.result, err
	}
	if !taskExists {
		return registration.result, ErrTaskAssetTaskNotFound
	}

	scoped := &AssetStore{db: registration.store.db, company: registration.store.company, tx: tx}
	for _, rule := range registration.rules {
		if err := registration.insert(tx, scoped, rule); err != nil {
			return registration.result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return registration.result, err
	}
	return registration.result, nil
}
func (registration *taskAssetScopeRegistration) prepare(inputs []ScopeInput) error {
	if registration.taskID <= 0 {
		return fmt.Errorf("%w: task id must be positive", ErrTaskAssetInvalid)
	}
	if len(inputs) == 0 {
		return fmt.Errorf("%w: scope is required", ErrTaskAssetInvalid)
	}
	if err := ValidateCompanyScopeInputBounds(inputs); err != nil {
		return fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}
	registration.rules = make([]ParsedScope, 0, len(inputs))
	for index, input := range inputs {
		rule, err := ParseScopeInput(input)
		if err != nil {
			return fmt.Errorf("%w: scope entry %d is invalid: %v", ErrTaskAssetInvalid, index+1, err)
		}
		registration.rules = append(registration.rules, rule)
	}
	if err := validateParsedScopeBounds(registration.rules); err != nil {
		return fmt.Errorf("%w: %v", ErrTaskAssetInvalid, err)
	}
	return nil
}
func (registration *taskAssetScopeRegistration) insert(tx *sql.Tx, scoped *AssetStore, rule ParsedScope) error {
	taskScope := TaskScope{
		TaskID: registration.taskID,
		Source: "manual",
		Reason: manualTaskScopeSummary,
	}
	var assetID int64
	switch rule.Kind {
	case "domain":
		taskScope.Kind = "root_domain"
		taskScope.Domain = rule.Domain
		var alreadyLinked bool
		err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='root_domain' AND domain=$1`, rule.Domain, registration.taskID).
			Scan(&assetID, &alreadyLinked)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		assetID, err = scoped.UpsertRootDomain(UpsertRootDomainReq{Domain: rule.Domain, TaskID: registration.taskID})
		if err != nil {
			return err
		}
		if alreadyLinked {
			registration.result.AssetsExisting++
		} else {
			registration.result.AssetsLinked++
		}
	case "ip":
		taskScope.Kind = "ip"
		taskScope.Net = rule.Net
		ip, _, parseErr := net.ParseCIDR(rule.Net)
		if parseErr != nil {
			return fmt.Errorf("%w: invalid IP: %s", ErrTaskAssetInvalid, rule.Raw)
		}
		ipValue := ip.String()
		var alreadyLinked bool
		err := tx.QueryRow(`SELECT id, $2=ANY(task_ids) FROM assets WHERE type='ip' AND ip=$1`, ipValue, registration.taskID).
			Scan(&assetID, &alreadyLinked)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		assetID, err = scoped.UpsertIP(UpsertIPReq{IP: ipValue, TaskID: registration.taskID})
		if err != nil {
			return err
		}
		if alreadyLinked {
			registration.result.AssetsExisting++
		} else {
			registration.result.AssetsLinked++
		}
	case "cidr":
		taskScope.Kind = "cidr"
		taskScope.Net = rule.Net
	case "icp", "keyword":
		taskScope.Kind = rule.Kind
		taskScope.Value = rule.Value
	default:
		return fmt.Errorf("%w: unsupported scope kind %q", ErrTaskAssetInvalid, rule.Kind)
	}

	if assetID > 0 {
		if err := scoped.SetTaskAssetSource(registration.taskID, assetID, "manual", manualTaskScopeSummary, nil); err != nil {
			return err
		}
	}
	inserted, err := scoped.upsertTaskScopeResult(taskScope)
	if err != nil {
		return err
	}
	if inserted {
		registration.result.ScopesAdded++
	} else {
		registration.result.ScopesExisting++
	}

	return nil
}
