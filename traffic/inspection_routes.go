package traffic

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

func inspectionRoutePrefixes(prefixes []string) ([]string, error) {
	if len(prefixes) == 0 {
		prefixes = []string{"/api", "/webapi"}
	}
	if len(prefixes) > 8 {
		return nil, errors.New("at most 8 route prefixes are allowed")
	}
	selected := make([]string, 0, len(prefixes))
	seen := make(map[string]bool)
	for _, raw := range prefixes {
		prefix := strings.TrimRight(strings.TrimSpace(raw), "/")
		if prefix == "" || !strings.HasPrefix(prefix, "/") || len(prefix) > 64 || strings.ContainsAny(prefix, "\r\n\t ") {
			return nil, fmt.Errorf("invalid route prefix %q", prefix)
		}
		if !seen[prefix] {
			seen[prefix] = true
			selected = append(selected, prefix)
		}
	}
	return selected, nil
}

// Index by first occurrence while counting every later occurrence. Candidate
// clipping happens after counting, so the retained routes keep full counts.
func inspectionRoutes(body []byte, prefixes []string, context int) []RouteCandidate {
	search, offsets := normalizeSlashEscapes(body)
	expressions := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		expressions = append(expressions, regexp.QuoteMeta(prefix)+`(?:/[A-Za-z0-9._~!$&+,:=@%?#=-]+)*`)
	}
	positions := regexp.MustCompile(strings.Join(expressions, "|")).FindAllIndex(search, -1)
	order := make(map[string]int)
	routes := make([]RouteCandidate, 0)
	for _, position := range positions {
		route := string(search[position[0]:position[1]])
		if query := strings.IndexAny(route, "?#"); query >= 0 {
			route = route[:query]
		}
		route = strings.TrimRight(route, ".,;:")
		if route == "" {
			continue
		}
		if index, exists := order[route]; exists {
			routes[index].Occurrences++
			continue
		}
		start, end := offsets[position[0]], offsets[position[1]]
		order[route] = len(routes)
		routes = append(routes, RouteCandidate{
			Route: route, Occurrences: 1, ByteOffset: start,
			Context: textSlice(body, maxInt(0, start-context), minInt(len(body), end+context)),
		})
	}
	return routes
}

func (inspection trafficInspection) routes(id string, prefixes []string, context, limit int) (RouteExtractionResult, error) {
	var err error
	if context, err = inspectionBound(context, 96, maxInspectContext, "context_bytes"); err != nil {
		return RouteExtractionResult{}, err
	}
	if limit, err = inspectionBound(limit, 50, maxRouteCandidates, "max_candidates"); err != nil {
		return RouteExtractionResult{}, err
	}
	selected, err := inspectionRoutePrefixes(prefixes)
	if err != nil {
		return RouteExtractionResult{}, err
	}
	body, err := inspection.store.loadInspectBody(id, "response")
	if err != nil {
		return RouteExtractionResult{}, err
	}
	if isBinaryBody(body.ContentType, body.Data) {
		return RouteExtractionResult{}, errors.New("response body is binary and cannot be route-extracted")
	}
	routes := inspectionRoutes(body.Data, selected, context)
	result := RouteExtractionResult{
		ExchangeID: strings.TrimSpace(id), BodySHA256: body.SHA256, BlobSHA256: body.BlobHash,
		BodyBytes: body.Total, Source: body.Source, Prefixes: selected,
		Candidates: []RouteCandidate{}, CandidatesTruncated: len(routes) > limit, SearchComplete: body.Complete,
	}
	result.Candidates = append(result.Candidates, routes[:minInt(limit, len(routes))]...)
	return result, nil
}
