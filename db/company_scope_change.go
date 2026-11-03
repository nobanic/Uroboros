package db

import (
	"database/sql"
	"errors"
	"fmt"
)

type companyChangeKind uint8

const (
	companyCreate companyChangeKind = iota
	companyScopeAdd
	companyScopeReplace
	companyRemove
	companyReattribute
)

type companyChange struct {
	kind         companyChangeKind
	companyID    int64
	name, logo   string
	reason       string
	inputs       []ScopeInput
	removeAssets bool
}

type companyChangeResult struct {
	companyID               int64
	added, skipped, invalid int
	validation              []string
	assetsRemoved           int64
}

// Failed transactions expose validation feedback, never provisional row IDs or mutation counts.
func (result companyChangeResult) failed(err error) (companyChangeResult, error) {
	return companyChangeResult{invalid: result.invalid, validation: result.validation}, err
}

func (request companyChange) scopeInput() bool {
	return request.kind == companyCreate || request.kind == companyScopeAdd || request.kind == companyScopeReplace
}

func (request companyChange) prepare() ([]ParsedScope, companyChangeResult, error) {
	var result companyChangeResult
	if !request.scopeInput() {
		return nil, result, nil
	}
	if err := ValidateCompanyScopeInputBounds(request.inputs); err != nil {
		return nil, result, err
	}
	rules, invalid, feedback := parseScopeInputs(request.inputs)
	result.invalid, result.validation = invalid, feedback
	if request.kind == companyScopeReplace && invalid > 0 {
		return nil, result, &CompanyScopeValidationError{Message: fmt.Sprintf(
			"the company scope contains %d invalid rules; the existing scope was not overwritten", invalid,
		)}
	}
	return rules, result, validateParsedScopeBounds(rules)
}

// applyCompanyChange serializes every mutation, keeps its checks and writes ordered,
// and rebuilds derived ownership only when the operation's original policy requires it.
func (store *CompanyStore) applyCompanyChange(request companyChange) (companyChangeResult, error) {
	rules, result, err := request.prepare()
	if err != nil {
		return result.failed(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		return result.failed(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := lockCompanyScopeMutation(tx); err != nil {
		return result.failed(err)
	}
	companyID := request.companyID
	switch request.kind {
	case companyCreate:
		var logo any
		if request.logo != "" {
			logo = request.logo
		}
		err = tx.QueryRow(`
INSERT INTO companies(name, nkey, logo)
VALUES ($1, $2, $3)
ON CONFLICT (nkey) DO NOTHING
RETURNING id`, request.name, companyNKey(request.name), logo).Scan(&companyID)
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrCompanyNameConflict
		}
		if err != nil {
			return result.failed(err)
		}
		result.companyID = companyID
	case companyScopeAdd, companyScopeReplace:
		if err := ensureCompanyExistsTx(tx, companyID); err != nil {
			return result.failed(err)
		}
		if request.kind == companyScopeReplace {
			if _, err := tx.Exec(`DELETE FROM company_scope WHERE company_id = $1`, companyID); err != nil {
				return result.failed(err)
			}
		}
	case companyRemove:
		if request.removeAssets {
			deleted, err := tx.Exec(`DELETE FROM assets WHERE company_id = $1`, companyID)
			if err != nil {
				return result.failed(err)
			}
			result.assetsRemoved, err = deleted.RowsAffected()
			if err != nil {
				return result.failed(err)
			}
		}
		deleted, err := tx.Exec(`DELETE FROM companies WHERE id = $1`, companyID)
		if err != nil {
			return result.failed(err)
		}
		count, err := deleted.RowsAffected()
		if err != nil {
			return result.failed(err)
		}
		if count == 0 {
			return result.failed(ErrCompanyNotFound)
		}
	case companyReattribute:
	default:
		return result.failed(fmt.Errorf("unsupported company mutation %d", request.kind))
	}
	needsAttribution := request.kind == companyScopeReplace || request.kind == companyRemove || request.kind == companyReattribute
	if request.scopeInput() {
		var insertedAttributable bool
		result.added, result.skipped, insertedAttributable, err = insertScopeRulesTx(tx, companyID, rules, request.reason)
		if err != nil {
			return result.failed(err)
		}
		needsAttribution = needsAttribution || insertedAttributable
	}
	if needsAttribution {
		warning, err := recomputeAttributionTx(tx)
		if err != nil {
			if request.kind == companyScopeAdd || request.kind == companyScopeReplace {
				err = fmt.Errorf("failed to recompute company attribution: %w", err)
			}
			return result.failed(err)
		}
		logAttributionWarning(warning)
	}
	if err := tx.Commit(); err != nil {
		return result.failed(err)
	}
	return result, nil
}
