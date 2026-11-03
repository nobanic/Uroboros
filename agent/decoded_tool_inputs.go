package agent

import "encoding/json"

// Reused worker handles retain their original anonymous structural identity.
type intentHandleInput = struct {
	IntentID json.RawMessage `json:"intent_id"`
}
