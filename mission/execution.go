package mission

// Interruption records why execution stopped. Operator actions have precedence
// over incidental provider errors observed while cancellation propagates.
type Interruption string

const (
	Uninterrupted      Interruption = ""
	OperatorPause      Interruption = "operator_pause"
	OperatorCancel     Interruption = "operator_cancel"
	MissionPause       Interruption = "mission_pause"
	DeadlineSettlement Interruption = "deadline_settlement"
	MissionFinished    Interruption = "mission_finished"
	PlannerStop        Interruption = "planner_stop"
)

type ExecutionResult struct {
	Interruption           Interruption
	Failed                 bool
	BudgetReached          bool
	ProviderQuotaExhausted bool
}

type WakeKind string

const (
	NoWake       WakeKind = ""
	GraphChanged WakeKind = "graph_changed"
	WorkFinished WakeKind = "work_finished"
)

type Settlement struct {
	State           string
	Wake            WakeKind
	ProviderBlocked bool
	Reason          string
}

// SettleExecution is the complete intent-outcome decision table. Adapters may
// persist this decision and emit it, but cannot reinterpret cancellation as a
// successful execution. Budgets never imply a proved goal.
func SettleExecution(result ExecutionResult) Settlement {
	switch result.Interruption {
	case OperatorPause:
		return Settlement{State: "paused", Reason: "operator paused the work"}
	case OperatorCancel:
		return Settlement{State: "paused", Reason: "awaiting operator cancellation cleanup"}
	case MissionPause:
		return Settlement{State: "open", Reason: "mission paused; preserve work for resume"}
	case DeadlineSettlement:
		return Settlement{State: "exhausted", Reason: "mission deadline settlement finished"}
	case MissionFinished:
		return Settlement{State: "stopped", Reason: "mission is terminal"}
	case PlannerStop:
		return Settlement{State: "stopped", Wake: GraphChanged, Reason: "planner stopped the work"}
	}
	if result.Failed {
		return Settlement{State: "blocked", Wake: WorkFinished, ProviderBlocked: result.ProviderQuotaExhausted, Reason: "execution failed"}
	}
	if result.BudgetReached {
		return Settlement{State: "exhausted", Wake: WorkFinished, Reason: "execution budget reached"}
	}
	return Settlement{State: "done", Wake: WorkFinished, Reason: "execution returned normally"}
}
