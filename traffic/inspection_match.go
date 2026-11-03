package traffic

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type bodyMatchPlan struct {
	document BodyMatchResult
	sides    []string
	matcher  *regexp.Regexp
	context  int
	limit    int
}

func prepareBodyMatch(id, side, pattern string, regexMode, caseSensitive bool, context, limit int) (bodyMatchPlan, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return bodyMatchPlan{}, errors.New("pattern is required")
	}
	if len(pattern) > maxInspectPattern {
		return bodyMatchPlan{}, fmt.Errorf("pattern exceeds %d bytes", maxInspectPattern)
	}
	var err error
	if context, err = inspectionBound(context, 160, maxInspectContext, "context_bytes"); err != nil {
		return bodyMatchPlan{}, err
	}
	if limit, err = inspectionBound(limit, 8, maxInspectMatches, "max_matches"); err != nil {
		return bodyMatchPlan{}, err
	}
	side = strings.ToLower(strings.TrimSpace(side))
	if side == "" {
		side = "response"
	}
	sides := []string{side}
	switch side {
	case "both":
		sides = []string{"request", "response"}
	case "request", "response":
	default:
		return bodyMatchPlan{}, errors.New("side must be request, response, or both")
	}
	expression := pattern
	if !regexMode {
		expression = regexp.QuoteMeta(expression)
	}
	if !caseSensitive {
		expression = "(?i)" + expression
	}
	matcher, err := regexp.Compile(expression)
	if err != nil {
		return bodyMatchPlan{}, fmt.Errorf("invalid RE2 pattern: %w", err)
	}
	if matcher.MatchString("") {
		return bodyMatchPlan{}, errors.New("pattern must not match an empty string")
	}
	return bodyMatchPlan{document: BodyMatchResult{
		ExchangeID: strings.TrimSpace(id), Pattern: pattern, Regex: regexMode,
		CaseSensitive: caseSensitive, Matches: []BodyMatch{}, Evidence: []BodyEvidence{}, SearchComplete: true,
	}, sides: sides, matcher: matcher, context: context, limit: limit}, nil
}

func (plan *bodyMatchPlan) collect(side string, body inspectBody) {
	out := &plan.document
	out.SearchComplete = out.SearchComplete && body.Complete
	out.Evidence = append(out.Evidence, BodyEvidence{
		Side: side, BodySHA256: body.SHA256, BlobSHA256: body.BlobHash,
		BodyBytes: body.Total, Source: body.Source, Complete: body.Complete,
	})
	available := plan.limit - len(out.Matches)
	positions := plan.matcher.FindAllIndex(body.Data, available+1)
	if len(positions) > available {
		out.MatchesTruncated = true
		positions = positions[:available]
	}
	for _, position := range positions {
		match := inspectionSpan(body.Data, position[0], position[1], plan.context, maxInspectContext)
		match.Side = side
		out.Matches = append(out.Matches, match)
	}
}

func (inspection trafficInspection) matches(id, side, pattern string, regexMode, caseSensitive bool, context, limit int) (BodyMatchResult, error) {
	plan, err := prepareBodyMatch(id, side, pattern, regexMode, caseSensitive, context, limit)
	if err != nil {
		return BodyMatchResult{}, err
	}
	for index, side := range plan.sides {
		body, err := inspection.store.loadInspectBody(plan.document.ExchangeID, side)
		if err != nil {
			return BodyMatchResult{}, err
		}
		if isBinaryBody(body.ContentType, body.Data) {
			return BodyMatchResult{}, fmt.Errorf("%s body is binary and cannot be text-searched", side)
		}
		plan.collect(side, body)
		if len(plan.document.Matches) == plan.limit {
			plan.document.MatchesTruncated = plan.document.MatchesTruncated || index+1 < len(plan.sides)
			break
		}
	}
	return plan.document, nil
}
