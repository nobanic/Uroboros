package traffic

import (
	"bytes"
	"fmt"
)

// Inspection borrows captures for one read. It neither executes a frontier nor
// promotes matches, routes or script references into findings.
type trafficInspection struct{ store *Traffic }

func inspectionBound(value, fallback, maximum int, field string) (int, error) {
	if value <= 0 {
		value = fallback
	}
	if value > maximum {
		return 0, fmt.Errorf("%s exceeds %d", field, maximum)
	}
	return value, nil
}

// All coordinates refer to captured bytes, including when the caller matched
// slash-normalized text. Only the displayed match may be independently capped.
func inspectionSpan(data []byte, start, end, context, matchLimit int) BodyMatch {
	visibleEnd := end
	if matchLimit > 0 && end-start > matchLimit {
		visibleEnd = start + matchLimit
	}
	lineStart := bytes.LastIndexByte(data[:start], '\n') + 1
	return BodyMatch{
		ByteStart: start, ByteEnd: end,
		Line: bytes.Count(data[:start], []byte{'\n'}) + 1, ByteColumn: start - lineStart + 1,
		Before: textSlice(data, maxInt(0, start-context), start),
		Match:  textSlice(data, start, visibleEnd), MatchTruncated: visibleEnd < end,
		After: textSlice(data, end, minInt(len(data), end+context)),
	}
}
