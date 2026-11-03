package db

import (
	"encoding/json"
)

// taskArchiveAggregate is the compact, non-sensitive statistics section of a
// cold archive manifest. Fields are additive so older archive formats remain
// readable when new dashboard dimensions are introduced.
type taskArchiveAggregate struct {
	TokenProfiles     []ProfileUsage    `json:"token_profiles"`
	TokenDaily        []ProfileDayUsage `json:"token_daily"`
	Skills            map[string]int    `json:"skills"`
	SkillStats        []SkillStat       `json:"skill_stats"`
	MissingSkillStats []SkillStat       `json:"missing_skill_stats"`
	Tools             map[string]int    `json:"tools"`
	FindingStats      FindingStats      `json:"finding_stats"`
}

func (d *DB) archivedTaskAggregates() ([]taskArchiveAggregate, error) {
	rawItems, err := d.ArchivedAggregateStats()
	if err != nil {
		return nil, err
	}
	out := make([]taskArchiveAggregate, 0, len(rawItems))
	for _, raw := range rawItems {
		var item taskArchiveAggregate
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func mergeAgentKeys(current, incoming []string) []string {
	seen := make(map[string]bool, len(current)+len(incoming))
	out := make([]string, 0, len(current)+len(incoming))
	for _, group := range [][]string{current, incoming} {
		for _, key := range group {
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func mergeArchivedSkillStats(live []SkillStat, archived []taskArchiveAggregate, missing bool) []SkillStat {
	return rollupArchivedSkills(live, archived, missing)
}
