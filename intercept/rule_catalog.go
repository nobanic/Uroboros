package intercept

import (
	"encoding/json"
	"regexp"

	"github.com/nobanic/UroborOS/db"
)

// Both catalog readers use the same lazy acquisition and original mutex. A nil
// rule slice remains unloaded, including when a successful query returned no rules.
func catalogValue[T any](i *Interceptor, read func() (T, bool)) (T, error) {
	i.mu.RLock()
	value, loaded := read()
	i.mu.RUnlock()
	if loaded {
		return value, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	value, loaded = read()
	if !loaded {
		if err := i.loadLocked(); err != nil {
			var zero T
			return zero, err
		}
		value, _ = read()
	}
	return value, nil
}

func compileInterceptRules(rows []db.InterceptRule) []compiledRule {
	var program []compiledRule
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		entry := compiledRule{InterceptRule: row}
		if row.MatchType == "regex" {
			pattern, err := regexp.Compile(row.Pattern)
			if err != nil {
				continue
			}
			entry.re = pattern
		}
		program = append(program, entry)
	}
	return program
}

// API reads keep configured order/duplicates and null versus malformed-array
// behavior. The cached membership view is built from this same decoded sequence.
func enabledToolNames(value string, stored bool) []string {
	if !stored {
		out := make([]string, len(defaultEnabledTools))
		copy(out, defaultEnabledTools)
		return out
	}
	var names []string
	if json.Unmarshal([]byte(value), &names) != nil {
		return []string{}
	}
	return names
}

func toolMembership(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

func decisionForRule(rule compiledRule) Decision {
	message := rule.Message
	if message == "" {
		message = defaultMessage(rule.Action, rule.Name)
	}
	encoded, _ := json.Marshal(rule.InterceptRule)
	return Decision{
		RuleName: rule.Name, ConfigDigest: digestInput(encoded),
		Action: rule.Action, Message: message, RuleID: rule.ID,
		TimeoutEnabled: rule.TimeoutEnabled, TimeoutSeconds: rule.TimeoutSeconds,
		TimeoutAction: rule.TimeoutAction,
	}
}
