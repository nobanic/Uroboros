package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/internal/toolinput"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/binaryartifact"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

func (s *Server) binaryTools() []actool.CoreTool {
	if s.binaryIntake == nil {
		return nil
	}
	return []actool.CoreTool{
		s.binaryTool("binary_artifact_list",
			"List content-recognized binary artifacts owned by this task. Returns metadata only; never raw bytes, host paths, scope, assets, facts, or findings.",
			objSchema(map[string]any{
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"offset": map[string]any{"type": "integer", "minimum": 0},
			}),
			func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
				return toolinput.Partial(input, func(arguments struct{ Limit, Offset int }) (actool.Result, error) {
					taskID, err := binaryToolTaskID(ctx)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					items, err := s.m.PG().ListBinaryArtifacts(ctx, taskID, arguments.Limit, arguments.Offset)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{"artifacts": items, "authority": "metadata_only"})

				})
			}),
		s.binaryTool("binary_triage_status",
			"Read deterministic offline binary-triage job status for this task. Omitting job_id lists recent jobs.",
			objSchema(map[string]any{
				"job_id": strParam("opaque binary-job id (optional)"),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"offset": map[string]any{"type": "integer", "minimum": 0},
			}),
			func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
				return toolinput.Partial(input, func(arguments binaryJobPageInput) (actool.Result, error) {
					taskID, err := binaryToolTaskID(ctx)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					if arguments.JobID != "" {
						job, err := s.m.PG().BinaryTriageJobForTask(ctx, taskID, arguments.JobID)
						if err != nil {
							return actool.Errorf(err.Error()), nil
						}
						return jsonResult(map[string]any{"job": job})
					}
					jobs, err := s.m.PG().ListBinaryTriageJobs(ctx, taskID, arguments.Limit, arguments.Offset)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{"jobs": jobs})

				})
			}),
		s.binaryTool("binary_triage_results",
			"Read bounded LIEF structural metadata for one task-owned job. Results are metadata/candidates only and never finding authority.",
			objSchema(map[string]any{
				"job_id": strParam("opaque binary-job id"),
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"offset": map[string]any{"type": "integer", "minimum": 0},
			}, "job_id"),
			func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
				return toolinput.Partial(input, func(arguments binaryJobPageInput) (actool.Result, error) {
					taskID, err := binaryToolTaskID(ctx)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					items, err := s.m.PG().ListBinaryObservations(ctx, taskID, arguments.JobID, arguments.Limit, arguments.Offset)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{"observations": items, "authority": "metadata_or_candidate_only"})

				})
			}),
		s.binaryTool("binary_triage_run",
			"Queue the fixed standard-v1 offline LIEF profile for one task-owned artifact. It cannot select tools, arguments, network access, or execution behavior.",
			objSchema(map[string]any{"artifact_id": strParam("opaque task-owned artifact id")}, "artifact_id"),
			func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
				return toolinput.Partial(input, func(arguments struct {
					ArtifactID string `json:"artifact_id"`
				}) (actool.Result, error) {
					taskID, err := binaryToolTaskID(ctx)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					job, err := s.binaryIntake.ScheduleArtifact(ctx, taskID, arguments.ArtifactID)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{"job": job, "profile_revision": "standard-v1"})

				})
			}),
		s.binaryTool("binary_triage_cancel",
			"Cancel one queued task-owned binary triage job. A running isolated worker is not interruptible through this B0 tool.",
			objSchema(map[string]any{"job_id": strParam("opaque queued binary-job id")}, "job_id"),
			func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
				return toolinput.Partial(input, func(arguments struct {
					JobID string `json:"job_id"`
				}) (actool.Result, error) {
					taskID, err := binaryToolTaskID(ctx)
					if err != nil {
						return actool.Errorf(err.Error()), nil
					}
					if err := s.m.PG().CancelBinaryTriageJob(ctx, taskID, arguments.JobID); err != nil {
						return actool.Errorf(err.Error()), nil
					}
					return jsonResult(map[string]any{"canceled": true, "job_id": arguments.JobID})

				})
			}),
	}
}

func binaryToolTaskID(ctx context.Context) (int64, error) {
	taskID := agent.RunInfoFrom(ctx).TaskID
	if taskID <= 0 {
		return 0, fmt.Errorf("binary triage requires a task-owned agent run")
	}
	return taskID, nil
}

func (s *Server) binaryTool(name, description string, schema map[string]any,
	call func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	guarded, err := binaryartifact.GuardTool(wrTool(name, description, schema, call))
	if err != nil {
		panic(err)
	}
	manifest, _ := binaryartifact.ManifestFor(name)
	return &binaryEffectAuthorizedTool{CoreTool: guarded, effects: manifest.Effects}
}

// binaryEffectAuthorizedTool is the host-owned grant boundary. Its effect list
// comes only from the immutable code manifest; tool JSON and database rows
// cannot add execute/network/filesystem authority.
type binaryEffectAuthorizedTool struct {
	actool.CoreTool
	effects []binaryartifact.Effect
}

func (tool *binaryEffectAuthorizedTool) CheckPermissions(ctx context.Context, input json.RawMessage, permissionContext permission.Context) permission.Decision {
	return tool.CoreTool.CheckPermissions(binaryartifact.WithAuthorizedEffects(ctx, tool.effects...), input, permissionContext)
}

func (tool *binaryEffectAuthorizedTool) Call(ctx context.Context, input json.RawMessage, toolContext *actool.ToolContext) (actool.Result, error) {
	return tool.CoreTool.Call(binaryartifact.WithAuthorizedEffects(ctx, tool.effects...), input, toolContext)
}
