package notify

import "fmt"

// channelConfigUpdate owns one edit's ordered destination/credential admission.
// Registry descriptors and the existing masking, comparison and merge rules
// remain authoritative. Caller maps are borrowed and never modified here.
type channelConfigUpdate struct {
	kind             string
	stored, incoming map[string]any
}

func (u channelConfigUpdate) merge() (map[string]any, error) {
	channel, ok := Get(u.kind)
	if !ok {
		return nil, fmt.Errorf("channel type %q is not registered", u.kind)
	}
	secrets, destinations := channel.SecretKeys(), channel.DestinationKeys()
	// Container checks must precede the unchanged-destination early return.
	if err := rejectMaskedInContainers(u.incoming, secrets); err != nil {
		return nil, err
	}
	changed := u.changedDestinations(destinations)
	if len(changed) == 0 {
		return MergeConfig(u.stored, u.incoming), nil
	}
	if missing := u.missingCredentials(secrets); len(missing) != 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(u.stored, u.incoming), nil
}

func (u channelConfigUpdate) changedDestinations(keys []string) []string {
	var changed []string
	for _, key := range keys {
		value, present := u.incoming[key]
		if !present {
			continue
		}
		if text, ok := value.(string); ok && IsMasked(text) {
			continue
		}
		if !sameConfigValue(value, u.stored[key]) {
			changed = append(changed, key)
		}
	}
	return changed
}

func (u channelConfigUpdate) missingCredentials(keys []string) []string {
	var missing []string
	for _, key := range keys {
		value, present := u.incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if text, ok := value.(string); ok && IsMasked(text) {
			missing = append(missing, key)
		}
	}
	return missing
}
