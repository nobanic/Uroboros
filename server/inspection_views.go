package server

import (
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/nobanic/UroborOS/db"
)

func (q inspectionQuery) stats() inspectionReply {
	out := inspectionStatistics{EngineMode: "idle", LLMConfigured: q.owner.engine.Ready()}
	if traffic := q.owner.m.Traffic(); traffic != nil {
		count, _ := traffic.Count()
		enabled := true
		out.Traffic = &count
		out.TrafficEnabled = &enabled
	}
	if counts, err := q.owner.m.Assets().CountsByType(); err == nil {
		total := 0
		for _, count := range counts {
			total += count
		}
		out.AssetCounts = &counts
		out.Assets = &total
	}
	task := q.task()
	if task == nil {
		if raw := q.values.Get("task"); raw != "" && raw != "active" {
			return inspectionStatus(404, "task not found")
		}
		return inspectionData(out)
	}
	out.LLMConfigured = q.owner.engine.ReadyFor(task)
	graphStats, _ := task.Store.Stats()
	out.Exploration = &graphStats
	intents, _ := task.Store.ListByKind(db.KindIntent, 100000)
	inFlight := 0
	for _, intent := range intents {
		if intent.State == "running" {
			inFlight++
		}
	}
	goals, _ := task.Store.ListByKind(db.KindGoal, 10000)
	met := 0
	for _, goal := range goals {
		if goal.State == "met" {
			met++
		}
	}
	last := q.owner.engine.LastActivity(task.ID)
	paused := q.owner.engine.IsPaused(task.ID)
	calls := q.owner.engine.ActiveLLMCalls(task.ID)
	running := q.owner.engine.ReadyFor(task) && q.owner.engine.Started(task.ID) && !paused
	stalled := running && calls == 0 && inFlight > 0 && last > 0 && time.Now().Unix()-last > 120
	out.EngineMode = inspectionEngineMode(paused, stalled, running, calls)
	out.ActiveTask = &inspectionActiveTask{ID: task.ID, Description: task.Description, Goal: task.Goal, Running: running, Paused: paused, EngineMode: out.EngineMode, InFlight: inFlight, LLMInFlight: calls, LastActivity: last, Stalled: stalled, GoalsTotal: len(goals), GoalsMet: met}
	return inspectionData(out)
}

func inspectionEngineMode(paused, stalled, running bool, calls int64) string {
	if paused {
		return "paused"
	}
	if stalled {
		return "stalled"
	}
	if running && calls > 0 {
		return "exploring"
	}
	return "idle"
}

func (q inspectionQuery) listTasks() inspectionReply {
	active := ""
	if task := q.owner.m.ActiveTask(); task != nil {
		active = task.ID
	}
	list := q.owner.m.List()
	metrics, err := q.owner.m.PG().TaskListMetricsAll()
	if err != nil {
		return inspectionStatus(500, "read authoritative task metrics: "+err.Error())
	}
	blockers, _ := q.owner.m.PG().TaskArchiveBlockers()
	rows := make([]TaskDTO, 0, len(list))
	for _, task := range list {
		dto := taskDTO(task, q.owner.resolvedTaskStatus(task))
		applyTaskArchiveBlocker(&dto, blockers)
		q.owner.applyTaskMetrics(&dto, task, metrics[task.ExpID])
		rows = append(rows, dto)
	}
	return inspectionData(inspectionTasks{Active: active, Tasks: rows})
}

func (q inspectionQuery) getTask() inspectionReply {
	task, found := q.pathTask()
	if !found {
		return inspectionStatus(404, "task not found")
	}
	dto := taskDTO(task, q.owner.resolvedTaskStatus(task))
	id, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		return inspectionStatus(500, "invalid task id: "+err.Error())
	}
	metric, err := q.owner.m.PG().TaskListMetricsForTask(id)
	if err != nil {
		return inspectionStatus(500, "read authoritative task metrics: "+err.Error())
	}
	q.owner.applyTaskMetrics(&dto, task, metric)
	blockers, _ := q.owner.m.PG().TaskArchiveBlockers()
	applyTaskArchiveBlocker(&dto, blockers)
	return inspectionData(dto)
}

func (q inspectionQuery) taskCoverage() inspectionReply {
	context := q.taskAssets()
	if context.failure != nil {
		return *context.failure
	}
	if !context.task.CoverageEnabled {
		return inspectionData(&db.Coverage{Enabled: false, ByType: []db.CoverageByType{}})
	}
	coverage, err := context.store.TaskCoverageWithSources(context.id)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	coverage.Enabled = true
	return inspectionData(coverage)
}
func (q inspectionQuery) taskCoverageGraph() inspectionReply {
	context := q.taskAssets()
	if context.failure != nil {
		return *context.failure
	}
	return inspectionResult(context.store.BuildCoverageGraph(context.id, context.task.ExpID))
}
func (q inspectionQuery) taskScopeList() inspectionReply {
	context := q.taskAssets()
	if context.failure != nil {
		return *context.failure
	}
	rows, err := context.store.ListTaskScopeWithSources(context.id)
	return inspectionResult(inspectionScope{Scope: rows}, err)
}
func (q inspectionQuery) taskAssetRefs() inspectionReply {
	task, found := q.pathTask()
	if !found {
		return inspectionStatus(404, "task not found")
	}
	assetID, _ := strconv.ParseInt(q.values.Get("asset_id"), 10, 64)
	if assetID <= 0 {
		return inspectionStatus(400, "asset_id is required")
	}
	refs, err := task.Store.AssetRefsWithSources(assetID)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	out := inspectionReferences{Facts: []CoverageAssetRefDTO{}, Findings: []CoverageAssetRefDTO{}, Intents: []CoverageAssetRefDTO{}}
	groups := map[string]*[]CoverageAssetRefDTO{db.KindIntent: &out.Intents, db.KindFact: &out.Facts, db.KindFinding: &out.Findings}
	for _, ref := range refs {
		if group := groups[ref.Kind]; group != nil {
			*group = append(*group, coverageAssetRefDTO(ref))
		}
	}
	return inspectionData(out)
}

func (q inspectionQuery) frontier() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionData([]any{})
	}
	rows, _ := task.Store.Frontier(q.integer("limit", 100))
	return inspectionData(taskNodeDTOs(rows))
}
func (q inspectionQuery) intents() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionData([]any{})
	}
	page := q.cursor(300)
	if !page.paged {
		rows, err := task.Store.ListByKind(db.KindIntent, page.limit)
		if err != nil {
			log.Printf("[intents] task=%s limit=%d: %v", task.ID, page.limit, err)
			return inspectionStatus(500, err.Error())
		}
		return inspectionData(taskNodeDTOs(rows))
	}
	rows, more, err := taskIntentHistoryPage(task.Store, page.before, page.limit, false, 0)
	if err != nil {
		log.Printf("[intents] task=%s before=%d limit=%d: %v", task.ID, page.before, page.limit, err)
		return inspectionStatus(500, err.Error())
	}
	sources, err := task.Store.DirectSourceStores()
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	for _, source := range sources {
		items, sourceMore, err := taskIntentHistoryPage(source.Store, page.before, page.limit, true, source.Task.TaskID)
		if err != nil {
			return inspectionStatus(500, err.Error())
		}
		rows = append(rows, items...)
		more = more || sourceMore
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID > rows[j].ID })
	if len(rows) > page.limit {
		rows = rows[:page.limit]
		more = true
	}
	return inspectionData(inspectionIntents{Items: taskNodeDTOs(rows), HasMore: more})
}

func (q inspectionQuery) explorationGraph() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionData(inspectionGraph{Nodes: []TaskNodeDTO{}, Edges: []EdgeDTO{}})
	}
	nodes, err := task.Store.Nodes(2000)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	edges, err := task.Store.Edges(5000)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	sources, err := task.Store.DirectSourceStores()
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	for _, source := range sources {
		sourceNodes, err := source.Store.Nodes(2000)
		if err != nil {
			return inspectionStatus(500, err.Error())
		}
		sourceEdges, err := source.Store.Edges(5000)
		if err != nil {
			return inspectionStatus(500, err.Error())
		}
		sourceNodes, sourceEdges = inheritedGraphSnapshot(sourceNodes, sourceEdges, source.Task.TaskID)
		nodes = append(nodes, sourceNodes...)
		edges = append(edges, sourceEdges...)
	}
	return inspectionData(inspectionGraph{Nodes: taskNodeDTOs(nodes), Edges: edgeDTOs(edges)})
}

func (q inspectionQuery) explorationNodes() inspectionReply {
	page, size := q.integer("page", 1), q.integer("size", 20)
	task := q.task()
	if task == nil {
		return inspectionData(inspectionBoard{Items: []TaskNodeDTO{}, Edges: []EdgeDTO{}, Refs: map[string]TaskNodeDTO{}, Page: page, Size: size})
	}
	nodes, total, err := task.Store.NodesPage(q.nodeFilter(), page, size)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	ids := make([]int64, 0, len(nodes))
	onPage := make(map[int64]bool, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
		onPage[node.ID] = true
	}
	edges, err := task.Store.EdgesTouching(ids)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	neighbours := map[int64]bool{}
	for _, edge := range edges {
		if !onPage[edge.From] {
			neighbours[edge.From] = true
		}
		if !onPage[edge.To] {
			neighbours[edge.To] = true
		}
	}
	neighbourIDs := make([]int64, 0, len(neighbours))
	for id := range neighbours {
		neighbourIDs = append(neighbourIDs, id)
	}
	related, err := task.Store.NodesByIDs(neighbourIDs)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	refs := make(map[string]TaskNodeDTO, len(related))
	for _, node := range related {
		refs[i64s(node.ID)] = taskNodeDTO(node)
	}
	items, edgeRows := taskNodeDTOs(nodes), edgeDTOs(edges)
	assets := q.owner.nodeAnchoredAssets(task, append(ids, neighbourIDs...))
	return inspectionData(inspectionBoard{Assets: &assets, Edges: edgeRows, Items: items, Page: page, Refs: refs, Size: size, Total: total})
}

func (q inspectionQuery) activity() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionData(inspectionActivity{Items: []ActivityDTO{}})
	}
	since, limit := int64(q.integer("since", 0)), q.integer("limit", 300)
	intent := q.optionalIntent()
	var rows []db.Activity
	var cursor int64
	var err error
	if intent == nil {
		rows, cursor, err = task.Store.ActivityList(nil, since, limit)
	} else {
		rows, cursor, err = task.Store.ActivityListWithSources(*intent, since, limit)
	}
	if err != nil {
		log.Printf("[activity] task=%s since=%d limit=%d intent=%v: %v", task.ID, since, limit, intent, err)
		return inspectionStatus(500, err.Error())
	}
	return inspectionData(inspectionActivity{Cursor: cursor, Items: activityDTOs(rows)})
}

func (q inspectionQuery) activityHistory() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionStatus(404, "task not found")
	}
	session := q.values.Get("session")
	filter, valid := parseActivitySession(session)
	if !valid {
		return inspectionStatus(400, "bad session")
	}
	if filter.Main && filter.MainSeg == nil {
		segment, err := task.Store.CurrentMainSeg()
		if err != nil {
			return inspectionStatus(500, err.Error())
		}
		filter.MainSeg = &segment
	}
	page := q.cursor(200)
	store, sourceID, err := activitySessionStore(task, filter)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	if store == nil {
		return inspectionStatus(404, "session not found")
	}
	snapshot, err := task.Store.ActivityMaxID()
	if err != nil {
		log.Printf("[activity/history] task=%s session=%s snapshot: %v", task.ID, session, err)
		return inspectionStatus(500, err.Error())
	}
	var rows []db.Activity
	var more bool
	if sourceID > 0 {
		rows, more, err = store.ActivityPageForTerminalIntent(*filter.NodeID, page.before, page.limit)
	} else {
		rows, more, err = store.ActivityPage(filter, page.before, page.limit)
	}
	if err != nil {
		log.Printf("[activity/history] task=%s session=%s before=%d limit=%d: %v", task.ID, session, page.before, page.limit, err)
		return inspectionStatus(500, err.Error())
	}
	if sourceID > 0 {
		for index := range rows {
			rows[index].SourceTaskID = sourceID
			rows[index].Inherited = true
		}
	}
	earliest := page.before
	if len(rows) > 0 {
		earliest = rows[0].ID
	}
	return inspectionData(inspectionHistory{EarliestCursor: earliest, HasMore: more, Items: activityDTOs(rows), SnapshotCursor: snapshot})
}

func (q inspectionQuery) activityDetail() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionStatus(404, "no task")
	}
	sequence, _ := strconv.ParseInt(q.sequence, 10, 64)
	detail, err := taskActivityDetail(task, sequence)
	return inspectionResult(inspectionDetail{Detail: detail}, err)
}
func (q inspectionQuery) tokenStats() inspectionReply {
	task := q.task()
	if task == nil {
		return inspectionData(inspectionTokens{Workers: []db.TokenUsage{}, Sessions: []db.SessionTokenUsage{}, Total: tokenTotalDTO(db.TokenUsage{})})
	}
	workers, err := task.Store.TokenStatsByWorker()
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	sessions, err := task.Store.TokenStatsBySession()
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	total, err := task.Store.TokenTotal()
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	return inspectionData(inspectionTokens{Workers: workers, Sessions: sessions, Total: tokenTotalDTO(total)})
}
func (q inspectionQuery) tokenDailyStats() inspectionReply {
	days := q.integer("days", 30)
	if q.owner.m.pg == nil {
		return inspectionData([]any{})
	}
	buckets, err := q.owner.m.pg.TokenDailyAll(days)
	if err != nil {
		return inspectionStatus(500, err.Error())
	}
	if buckets == nil {
		buckets = []db.DailyTokenBucket{}
	}
	return inspectionData(buckets)
}
func (q inspectionQuery) conversationTokens() inspectionReply {
	if q.owner.m.pg == nil {
		return inspectionData([]any{})
	}
	return inspectionResult(q.owner.m.pg.ConversationTokenSummaries())
}
