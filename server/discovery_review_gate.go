package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nobanic/UroborOS/agent"
	actool "github.com/nobanic/norma/tool"
)

type discoveryReviewChecker interface {
	DiscoveryReviewRequired(context.Context, int64) (bool, error)
	DiscoveryFamilyAllowed(context.Context, int64, string) (bool, error)
}

type discoveryReviewEnsurer interface {
	EnsureCurrentDiscoveryReview(context.Context, int64) error
}

type discoveryGuardedTool struct {
	actool.CoreTool
	checker  discoveryReviewChecker
	agentKey string
	ri       agent.RunInfo
}

func guardDiscoveryWorkflowTool(t actool.CoreTool, checker discoveryReviewChecker, agentKey string, ri agent.RunInfo) actool.CoreTool {
	if t == nil || checker == nil {
		return t
	}
	return &discoveryGuardedTool{CoreTool: t, checker: checker, agentKey: agentKey, ri: ri}
}

func discoveryScannerFamily(toolName string, input json.RawMessage) (string, bool, error) {
	switch toolName {
	case "katana_run":
		return "katana_rerun", true, nil
	case "arsenal_discover":
		return "arsenal_discover", true, nil
	case "arjun_discover":
		return "arjun_discover", true, nil
	case "waf_fingerprint":
		return "waf_fingerprint", true, nil
	case "archive_discover":
		return "archive_discover", true, nil
	case "arsenal_run":
		var args struct {
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", true, fmt.Errorf("invalid Arsenal request: %w", err)
		}
		switch args.Tool {
		case "httpx", "ffuf", "nuclei":
			return args.Tool, true, nil
		default:
			return "", true, fmt.Errorf("unknown typed Arsenal family %q", args.Tool)
		}
	default:
		return "", false, nil
	}
}

func (t *discoveryGuardedTool) Call(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	ri := mergeRunInfo(t.ri, agent.RunInfoFrom(ctx))
	if ri.TaskID <= 0 {
		return t.CoreTool.Call(ctx, input, tc)
	}
	name := t.Name()
	if t.agentKey == "planner" && (name == "add_intent" || name == "prove_goal" || name == "goal_met") {
		if ensurer, ok := t.checker.(discoveryReviewEnsurer); ok {
			if err := ensurer.EnsureCurrentDiscoveryReview(ctx, ri.TaskID); err != nil {
				return actool.Errorf("discovery coverage gate is unavailable"), nil
			}
		}
		required, err := t.checker.DiscoveryReviewRequired(ctx, ri.TaskID)
		if err != nil {
			return actool.Errorf("discovery coverage gate is unavailable"), nil
		}
		if required {
			return actool.Errorf("automatic discovery has not been reviewed for the current snapshot; call discovery_review, wait for terminal Katana state, then record_discovery_decision"), nil
		}
	}
	family, scanner, err := discoveryScannerFamily(name, input)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	if scanner {
		if ensurer, ok := t.checker.(discoveryReviewEnsurer); ok {
			if err := ensurer.EnsureCurrentDiscoveryReview(ctx, ri.TaskID); err != nil {
				return actool.Errorf("discovery coverage gate is unavailable"), nil
			}
		}
		allowed, err := t.checker.DiscoveryFamilyAllowed(ctx, ri.TaskID, family)
		if err != nil {
			return actool.Errorf("discovery coverage gate is unavailable"), nil
		}
		if !allowed {
			return actool.Errorf(fmt.Sprintf("typed discovery family %s is not authorized by the current coverage decision", family)), nil
		}
	}
	return t.CoreTool.Call(ctx, input, tc)
}
