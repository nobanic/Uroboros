package server

import (
	"net/http"
	"net/url"

	"github.com/nobanic/UroborOS/db"
)

// Finding requests borrow the operator's existing manager and database. Reads
// and ordered metadata edits share response handling, not finding authority.
type findingRequest struct {
	server *Server
	http   *http.Request
	query  url.Values
}

func (s *Server) findingRequest(r *http.Request) findingRequest {
	return findingRequest{server: s, http: r, query: r.URL.Query()}
}

type findingResponse struct {
	code    int
	message string
	value   any
}

func findingFailure(code int, message string) findingResponse {
	return findingResponse{code: code, message: message}
}

func findingDocument(value any, err error) findingResponse {
	if err != nil {
		return findingFailure(http.StatusInternalServerError, err.Error())
	}
	return findingResponse{code: http.StatusOK, value: value}
}

func writeFinding(w http.ResponseWriter, reply findingResponse) {
	if reply.code >= http.StatusBadRequest {
		writeErr(w, reply.code, reply.message)
	} else {
		writeJSON(w, reply.code, reply.value)
	}
}

func (q findingRequest) id() int64 {
	return int64(atoiDefault(q.http.PathValue("id"), 0))
}

func (q findingRequest) record() (*db.DBFinding, findingResponse) {
	if q.id() <= 0 {
		return nil, findingFailure(400, "bad finding id")
	}
	record, err := q.server.m.pg.GetFinding(q.id())
	switch {
	case err != nil:
		return nil, findingDocument(nil, err)
	case record == nil:
		return nil, findingFailure(404, "finding not found")
	default:
		return record, findingResponse{}
	}
}

func (q findingRequest) page(defaultSize, maximum int) (int, int) {
	return findingPaginationParam(q.query.Get("page"), 1, 0),
		findingPaginationParam(q.query.Get("limit"), defaultSize, maximum)
}

func (s *Server) findingRows(rows []*db.DBFinding) []FindingDTO {
	assets := s.resolveFindingAssets(rows)
	view := make([]FindingDTO, 0, len(rows))
	for _, record := range rows {
		view = append(view, findingFromDB(record, assets))
	}
	return view
}

func (s *Server) findingNodeAssets(meta map[int64]db.FindingMeta) map[int64]*db.Asset {
	var ids []int64
	for _, entry := range meta {
		ids = append(ids, entry.AssetIDs...)
	}
	return s.resolveAssetIDs(ids)
}
