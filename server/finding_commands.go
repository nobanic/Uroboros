package server

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

// An alias retains the anonymous JSON field/type error spelling of the HTTP
// contract. Nil pointers represent both null and absent fields.
type findingMetadataPatch = struct {
	Status    *string `json:"status"`
	Severity  *string `json:"severity"`
	Name      *string `json:"name"`
	VulnClass *string `json:"vulnclass"`
}

type findingMetadataEdit struct {
	value   *string
	valid   func(string) bool
	problem string
	trim    bool
	write   func(int64, string) (int64, error)
}

func findingAffected(count int64, err error) findingResponse {
	if err != nil {
		return findingDocument(nil, err)
	}
	if count == 0 {
		return findingFailure(404, "finding not found")
	}
	return findingResponse{}
}

func (edit findingMetadataEdit) apply(id int64) findingResponse {
	if edit.value == nil {
		return findingResponse{}
	}
	value := *edit.value
	if edit.valid != nil && !edit.valid(value) {
		return findingFailure(400, edit.problem+value)
	}
	if edit.trim {
		value = strings.TrimSpace(value)
	}
	return findingAffected(edit.write(id, value))
}

func (q findingRequest) status(id int64, value string) findingResponse {
	if !db.ValidFindingStatus(value) {
		return findingFailure(400, "bad status: "+value)
	}
	from, found, notified, err := q.server.m.pg.SetFindingStatusWithNotify(q.http.Context(), id, value)
	if err != nil {
		return findingDocument(nil, err)
	}
	if !found {
		return findingFailure(404, "finding not found")
	}
	if !notified && from != value {
		log.Printf("[notify] status-change event not recorded finding=%d %s→%s (status already updated)", id, from, value)
	}
	return findingResponse{}
}

func (q findingRequest) patch() findingResponse {
	id := q.id()
	if id <= 0 {
		return findingFailure(400, "bad finding id")
	}
	var patch findingMetadataPatch
	if err := json.NewDecoder(q.http.Body).Decode(&patch); err != nil {
		return findingFailure(400, "bad json: "+err.Error())
	}
	if patch.Status == nil && patch.Severity == nil && patch.Name == nil && patch.VulnClass == nil {
		return findingFailure(400, "nothing to update: provide status/severity/name/vulnclass")
	}
	// Validation and writes are deliberately interleaved. Earlier changes and
	// their notification survive a later invalid field or repository failure.
	if patch.Status != nil {
		if result := q.status(id, *patch.Status); result.code != 0 {
			return result
		}
	}
	store := q.server.m.pg
	ordered := [...]findingMetadataEdit{
		{value: patch.Severity, valid: db.ValidSeverity, problem: "bad severity: ", write: store.SetFindingSeverity},
		{value: patch.Name, trim: true, write: store.SetFindingName},
		{value: patch.VulnClass, trim: true, write: store.SetFindingVulnClass},
	}
	for _, edit := range ordered {
		if result := edit.apply(id); result.code != 0 {
			return result
		}
	}
	record, failure := q.record()
	if record == nil {
		return failure
	}
	return findingDocument(findingFromDB(record, q.server.resolveAssetIDs(record.AssetIDs)), nil)
}

func (q findingRequest) delete() findingResponse {
	id := q.id()
	if id <= 0 {
		return findingFailure(400, "bad finding id")
	}
	if result := findingAffected(q.server.m.pg.DeleteFinding(id)); result.code != 0 {
		return result
	}
	return findingDocument(map[string]any{"deleted": true, "id": id}, nil)
}
