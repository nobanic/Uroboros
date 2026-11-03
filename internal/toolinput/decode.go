package toolinput

import (
	"encoding/json"
	actool "github.com/nobanic/norma/tool"
)

// Decode retains the legacy checked-error policy before invoking typed effects.
func Decode[T any](raw json.RawMessage, apply func(T) (actool.Result, error)) (actool.Result, error) {
	return read(raw, apply, true)
}

// Partial retains the legacy permissive policy, including partially decoded values.
func Partial[T any](raw json.RawMessage, apply func(T) (actool.Result, error)) (actool.Result, error) {
	return read(raw, apply, false)
}
func read[T any](raw json.RawMessage, apply func(T) (actool.Result, error), checked bool) (actool.Result, error) {
	var input T
	err := json.Unmarshal(raw, &input)
	if checked && err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return apply(input)
}
