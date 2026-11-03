package db

import (
	"database/sql"
	"strings"
	"time"
)

type usageDashboard struct{ database *DB }

// The finalizer chooses the original late-error contract and runs while the
// query's deferred close still belongs to this reader. Scan/query failures
// always discard the result; recent/task lists alone retain late partial rows.
func readUsageRows[T, R any](database *DB, query string, args []any, targets func(*T) []any, finish func([]T, error) (R, error)) (R, error) {
	var empty R
	rows, err := database.Query(query, args...)
	if err != nil {
		return empty, err
	}
	defer rows.Close()
	values := []T{}
	finished, err := visitRows(rows, func() error {
		var value T
		if err := rows.Scan(targets(&value)...); err != nil {
			return err
		}
		values = append(values, value)
		return nil
	})
	if !finished {
		return empty, err
	}
	return finish(values, err)
}

func usageColdView[T, R any](database *DB, combine func([]T, []taskArchiveAggregate) R) func([]T, error) (R, error) {
	return func(live []T, readError error) (R, error) {
		var empty R
		if readError != nil {
			return empty, readError
		}
		archived, err := database.archivedTaskAggregates()
		if err != nil {
			return empty, err
		}
		return combine(live, archived), nil
	}
}

func (view usageDashboard) profiles() ([]ProfileUsage, error) {
	return readUsageRows(view.database, usageProfilesSQL, nil, func(row *ProfileUsage) []any {
		return []any{&row.ProfileName, &row.Calls, &row.Tasks, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens, &row.CacheWriteTokens}
	}, usageColdView(view.database, rollupUsageProfiles))
}

func (view usageDashboard) daily(days int) ([]ProfileDayUsage, error) {
	if days <= 0 {
		days = 365
	}
	return readUsageRows(view.database, usageDailySQL, []any{days}, func(row *ProfileDayUsage) []any {
		return []any{&row.ProfileName, &row.Date, &row.InputTokens, &row.OutputTokens, &row.CacheReadTokens}
	}, usageColdView(view.database, func(live []ProfileDayUsage, archived []taskArchiveAggregate) []ProfileDayUsage {
		cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
		return rollupUsageDays(live, archived, cutoff)
	}))
}

type usageSkillRow struct {
	stat   SkillStat
	agents string
	last   sql.NullTime
}

func usageSkillTargets(row *usageSkillRow, missing, task bool) []any {
	columns := []any{&row.stat.Skill, &row.stat.Calls}
	if !missing && !task {
		columns = append(columns, &row.stat.Tasks)
	}
	if !task {
		columns = append(columns, &row.agents)
	}
	return append(columns, &row.last)
}

func usageSkills(rows []usageSkillRow) []SkillStat {
	result := make([]SkillStat, 0, len(rows))
	for _, row := range rows {
		row.stat.Agents = []string{}
		if row.agents != "" {
			row.stat.Agents = strings.Split(row.agents, ",")
		}
		if row.last.Valid {
			stamp := row.last.Time
			row.stat.LastUsed = &stamp
		}
		result = append(result, row.stat)
	}
	return result
}

func (view usageDashboard) skills(missing bool, limit int) ([]SkillStat, error) {
	if missing && (limit <= 0 || limit > 200) {
		limit = 20
	}
	return readUsageRows(view.database, usageSkillsSQL(missing), nil, func(row *usageSkillRow) []any {
		return usageSkillTargets(row, missing, false)
	}, usageColdView(view.database, func(live []usageSkillRow, archived []taskArchiveAggregate) []SkillStat {
		result := rollupArchivedSkills(usageSkills(live), archived, missing)
		if missing && len(result) > limit {
			result = result[:limit]
		}
		return result
	}))
}

func (view usageDashboard) recentSkill(skill string, limit int) ([]SkillCall, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return readUsageRows(view.database, usageRecentSkillSQL, []any{skill, limit}, func(row *SkillCall) []any {
		return []any{&row.TS, &row.AgentKey, &row.TaskID, &row.SessionID, &row.ArgsLen}
	}, func(rows []SkillCall, err error) ([]SkillCall, error) { return rows, err })
}

func (view usageDashboard) taskSkills(task int64) ([]SkillStat, error) {
	return readUsageRows(view.database, usageTaskSkillsSQL, []any{task}, func(row *usageSkillRow) []any {
		return usageSkillTargets(row, false, true)
	}, func(rows []usageSkillRow, err error) ([]SkillStat, error) { return usageSkills(rows), err })
}

type usageToolCount struct {
	key   string
	calls int
}

func (view usageDashboard) tools() (map[string]int, error) {
	return readUsageRows(view.database, usageToolsSQL, nil, func(row *usageToolCount) []any {
		return []any{&row.key, &row.calls}
	}, usageColdView(view.database, func(live []usageToolCount, archived []taskArchiveAggregate) map[string]int {
		result := map[string]int{}
		for _, row := range live {
			result[row.key] = row.calls
		}
		for _, archive := range archived {
			for key, calls := range archive.Tools {
				result[key] += calls
			}
		}
		return result
	}))
}

func usageSkillsSQL(missing bool) string {
	var query strings.Builder
	query.WriteString("\nSELECT skill, COUNT(*) AS calls,\n")
	if !missing {
		query.WriteString("       COUNT(DISTINCT task_id) AS tasks,\n")
	}
	query.WriteString("       COALESCE(STRING_AGG(DISTINCT agent_key, ','), '') AS agents,\n       MAX(ts) AS last_used\nFROM skill_usage\nWHERE ")
	if missing {
		query.WriteString("NOT ")
	}
	query.WriteString("found\nGROUP BY skill\nORDER BY COUNT(*) DESC, skill")
	return query.String()
}

const usageProfilesSQL = `
SELECT COALESCE(profile_name,'') AS profile_name, COUNT(*) AS calls,
       COUNT(DISTINCT task_id) AS tasks,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
GROUP BY profile_name
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC`

const usageDailySQL = `
SELECT COALESCE(profile_name,'') AS profile_name,
       to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0)
FROM llm_usage
WHERE ts >= now() - ($1 * interval '1 day')
GROUP BY profile_name, day
ORDER BY day`

const usageRecentSkillSQL = `
SELECT ts, COALESCE(agent_key,''), COALESCE(task_id,0), COALESCE(session_id,''), args_len
FROM skill_usage
WHERE skill = $1 AND found
ORDER BY ts DESC
LIMIT $2`

const usageTaskSkillsSQL = `
SELECT skill, COUNT(*) AS calls, MAX(ts) AS last_used
FROM skill_usage
WHERE task_id = $1 AND found
GROUP BY skill
ORDER BY COUNT(*) DESC, skill`

const usageToolsSQL = `
SELECT tool_key, COUNT(*)
FROM tool_usage
GROUP BY tool_key`
