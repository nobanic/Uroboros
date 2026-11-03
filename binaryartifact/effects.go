// Package binaryartifact defines the code-owned contract for binary artifacts.
//
// The database may decide whether a tool is visible, enabled, and bound to an
// agent. It cannot change the effects declared here. Every binary-artifact tool
// must be wrapped with GuardTool, which checks the effects both in the normal
// permission path and again in Call so a direct transport invocation cannot
// bypass the boundary.
package binaryartifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

// Effect is a backend-enforced capability class. Values are deliberately
// closed: adding a new class requires a source change and review.
type Effect string

const (
	EffectRead          Effect = "read"
	EffectControlJob    Effect = "control_job"
	EffectAnalyzeStatic Effect = "analyze_static"
	EffectAnnotate      Effect = "annotate"
	EffectMutateProject Effect = "mutate_project"
	EffectExecuteSample Effect = "execute_sample"
	EffectNetwork       Effect = "network"
	EffectFilesystem    Effect = "filesystem"
)

var knownEffects = map[Effect]struct{}{
	EffectRead: {}, EffectControlJob: {}, EffectAnalyzeStatic: {},
	EffectAnnotate: {}, EffectMutateProject: {}, EffectExecuteSample: {},
	EffectNetwork: {}, EffectFilesystem: {},
}

// ToolManifest is immutable code-owned metadata for one model-facing tool.
// Effects is returned as a copy by ManifestFor so callers cannot alter the
// registry through a shared slice.
type ToolManifest struct {
	Name    string   `json:"name"`
	Effects []Effect `json:"effects"`
}

var toolManifests = map[string]ToolManifest{
	"binary_artifact_list":  {Name: "binary_artifact_list", Effects: []Effect{EffectRead}},
	"binary_triage_status":  {Name: "binary_triage_status", Effects: []Effect{EffectRead}},
	"binary_triage_results": {Name: "binary_triage_results", Effects: []Effect{EffectRead}},
	"binary_triage_run":     {Name: "binary_triage_run", Effects: []Effect{EffectAnalyzeStatic}},
	"binary_triage_cancel":  {Name: "binary_triage_cancel", Effects: []Effect{EffectControlJob}},
}

var (
	ErrUnknownTool     = errors.New("binary artifact tool has no code-owned effect manifest")
	ErrEffectDenied    = errors.New("binary artifact tool effect denied")
	ErrInvalidManifest = errors.New("invalid binary artifact effect manifest")
)

// ManifestFor returns the code-owned manifest for name.
func ManifestFor(name string) (ToolManifest, bool) {
	manifest, ok := toolManifests[name]
	if !ok {
		return ToolManifest{}, false
	}
	manifest.Effects = append([]Effect(nil), manifest.Effects...)
	return manifest, true
}

// Manifests returns a stable snapshot suitable for operator inspection. It is
// not used as an authorization input.
func Manifests() []ToolManifest {
	names := make([]string, 0, len(toolManifests))
	for name := range toolManifests {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ToolManifest, 0, len(names))
	for _, name := range names {
		manifest, _ := ManifestFor(name)
		out = append(out, manifest)
	}
	return out
}

// ValidateManifest rejects empty, unknown, or duplicate effects. It is useful
// for startup/self-tests while the actual registry remains unexported.
func ValidateManifest(manifest ToolManifest) error {
	if manifest.Name == "" || len(manifest.Effects) == 0 {
		return fmt.Errorf("%w: name and effects are required", ErrInvalidManifest)
	}
	seen := make(map[Effect]struct{}, len(manifest.Effects))
	for _, effect := range manifest.Effects {
		if _, ok := knownEffects[effect]; !ok {
			return fmt.Errorf("%w: unknown effect %q", ErrInvalidManifest, effect)
		}
		if _, ok := seen[effect]; ok {
			return fmt.Errorf("%w: duplicate effect %q", ErrInvalidManifest, effect)
		}
		seen[effect] = struct{}{}
	}
	return nil
}

type authorizedEffectsKey struct{}

// WithAuthorizedEffects adds a host-owned execution grant to ctx. The grant is
// intentionally contextual and never comes from tool JSON, the tools table, or
// a model-controlled parameter.
func WithAuthorizedEffects(ctx context.Context, effects ...Effect) context.Context {
	grants := make(map[Effect]struct{}, len(effects))
	for _, effect := range effects {
		if _, ok := knownEffects[effect]; ok {
			grants[effect] = struct{}{}
		}
	}
	return context.WithValue(ctx, authorizedEffectsKey{}, grants)
}

func authorize(ctx context.Context, manifest ToolManifest) error {
	grants, _ := ctx.Value(authorizedEffectsKey{}).(map[Effect]struct{})
	for _, effect := range manifest.Effects {
		if _, ok := grants[effect]; !ok {
			return fmt.Errorf("%w: %s requires %s", ErrEffectDenied, manifest.Name, effect)
		}
	}
	return nil
}

// GuardTool binds a tool to its immutable manifest. Unknown tool names fail at
// construction instead of silently receiving a weaker default.
func GuardTool(tool actool.CoreTool) (actool.CoreTool, error) {
	if tool == nil {
		return nil, fmt.Errorf("%w: nil tool", ErrUnknownTool)
	}
	manifest, ok := ManifestFor(tool.Name())
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, tool.Name())
	}
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	return &effectGuard{CoreTool: tool, manifest: manifest}, nil
}

type effectGuard struct {
	actool.CoreTool
	manifest ToolManifest
}

func (guard *effectGuard) CheckPermissions(ctx context.Context, input json.RawMessage, pc permission.Context) permission.Decision {
	if err := authorize(ctx, guard.manifest); err != nil {
		return permission.Denied(err.Error())
	}
	return guard.CoreTool.CheckPermissions(ctx, input, pc)
}

func (guard *effectGuard) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if err := authorize(ctx, guard.manifest); err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return guard.CoreTool.Call(ctx, input, tc)
}
