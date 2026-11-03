package db

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Ordered text policies are shared by complete input and partial updates.
// Input normalizes all fields before validation; PATCH commits each normalized
// pointer only after that field passes and drops the key on any later failure.
type taskTemplateTextPolicy struct {
	name      string
	limit     int
	normalize func(string) string
}

func taskTemplateTextPolicies() [3]taskTemplateTextPolicy {
	return [3]taskTemplateTextPolicy{
		{"name", MaxTaskTemplateNameRunes, taskTemplateName},
		{"description", MaxTaskTemplateTextRunes, strings.TrimSpace},
		{"goal", MaxTaskTemplateTextRunes, strings.TrimSpace},
	}
}
func (policy taskTemplateTextPolicy) validate(value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrTaskTemplateInvalid, policy.name)
	}
	if utf8.RuneCountInString(value) > policy.limit {
		return fmt.Errorf("%w: %s exceeds %d characters", ErrTaskTemplateInvalid, policy.name, policy.limit)
	}
	return nil
}
func taskTemplateInputFields(input TaskTemplateInput) (TaskTemplateInput, string, error) {
	policies := taskTemplateTextPolicies()
	targets := [3]*string{&input.Name, &input.Description, &input.Goal}
	for index, policy := range policies {
		*targets[index] = policy.normalize(*targets[index])
	}
	for index, policy := range policies {
		if failure := policy.validate(*targets[index]); failure != nil {
			return input, "", failure
		}
	}
	return input, taskTemplateNKey(input.Name), nil
}
func taskTemplatePatchFields(patch TaskTemplatePatch) (TaskTemplatePatch, *string, error) {
	if patch.Name == nil && patch.Description == nil && patch.Goal == nil && !patch.SetCategoryID && !patch.SetInterceptRules {
		return patch, nil, fmt.Errorf("%w: no fields supplied", ErrTaskTemplateInvalid)
	}
	targets := [3]**string{&patch.Name, &patch.Description, &patch.Goal}
	var key *string
	for index, policy := range taskTemplateTextPolicies() {
		target := targets[index]
		if *target == nil {
			continue
		}
		normalized := policy.normalize(**target)
		if failure := policy.validate(normalized); failure != nil {
			return patch, nil, failure
		}
		*target = &normalized
		if index == 0 {
			identity := taskTemplateNKey(normalized)
			key = &identity
		}
	}
	return patch, key, nil
}
