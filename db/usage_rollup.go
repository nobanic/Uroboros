package db

import "sort"

// Live SQL groups are authoritative seeds, including duplicate-key overwrite.
// Cold summaries then contribute additively. Callers choose their result storage
// so private live token slices can be reused without mutating borrowed skills.
type usageGroups[T any] struct {
	values map[string]T
	key    func(T) string
}

func groupUsage[T any](live []T, key func(T) string) usageGroups[T] {
	group := usageGroups[T]{values: make(map[string]T, len(live)), key: key}
	for _, value := range live {
		group.values[key(value)] = value
	}
	return group
}

func (group usageGroups[T]) add(value T, combine func(T, T) T) {
	key := group.key(value)
	group.values[key] = combine(group.values[key], value)
}

func (group usageGroups[T]) sorted(out []T, less func(T, T) bool) []T {
	for _, value := range group.values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func rollupUsageProfiles(live []ProfileUsage, archived []taskArchiveAggregate) []ProfileUsage {
	group := groupUsage(live, func(value ProfileUsage) string { return value.ProfileName })
	for _, archive := range archived {
		for _, value := range archive.TokenProfiles {
			group.add(value, func(current, cold ProfileUsage) ProfileUsage {
				current.ProfileName = cold.ProfileName
				current.Calls += cold.Calls
				current.Tasks += cold.Tasks
				current.InputTokens += cold.InputTokens
				current.OutputTokens += cold.OutputTokens
				current.CacheReadTokens += cold.CacheReadTokens
				current.CacheWriteTokens += cold.CacheWriteTokens
				return current
			})
		}
	}
	return group.sorted(live[:0], func(left, right ProfileUsage) bool {
		l, r := left.InputTokens+left.OutputTokens, right.InputTokens+right.OutputTokens
		if l != r {
			return l > r
		}
		return left.ProfileName < right.ProfileName
	})
}

func rollupUsageDays(live []ProfileDayUsage, archived []taskArchiveAggregate, cutoff string) []ProfileDayUsage {
	group := groupUsage(live, func(value ProfileDayUsage) string { return value.ProfileName + "\x00" + value.Date })
	for _, archive := range archived {
		for _, value := range archive.TokenDaily {
			if value.Date < cutoff {
				continue
			}
			group.add(value, func(current, cold ProfileDayUsage) ProfileDayUsage {
				current.ProfileName, current.Date = cold.ProfileName, cold.Date
				current.InputTokens += cold.InputTokens
				current.OutputTokens += cold.OutputTokens
				current.CacheReadTokens += cold.CacheReadTokens
				return current
			})
		}
	}
	return group.sorted(live[:0], func(left, right ProfileDayUsage) bool {
		if left.Date != right.Date {
			return left.Date < right.Date
		}
		return left.ProfileName < right.ProfileName
	})
}

func rollupArchivedSkills(live []SkillStat, archived []taskArchiveAggregate, missing bool) []SkillStat {
	group := groupUsage(live, func(value SkillStat) string { return value.Skill })
	for _, archive := range archived {
		cold := archive.SkillStats
		if missing {
			cold = archive.MissingSkillStats
		} else if len(cold) == 0 {
			for skill, calls := range archive.Skills {
				cold = append(cold, SkillStat{Skill: skill, Calls: calls, Tasks: 1})
			}
		}
		for _, value := range cold {
			group.add(value, func(current, incoming SkillStat) SkillStat {
				current.Skill = incoming.Skill
				current.Calls += incoming.Calls
				current.Tasks += incoming.Tasks
				current.Agents = mergeAgentKeys(current.Agents, incoming.Agents)
				if incoming.LastUsed != nil && (current.LastUsed == nil || incoming.LastUsed.After(*current.LastUsed)) {
					current.LastUsed = incoming.LastUsed
				}
				return current
			})
		}
	}
	return group.sorted(make([]SkillStat, 0, len(group.values)), func(left, right SkillStat) bool {
		if left.Calls != right.Calls {
			return left.Calls > right.Calls
		}
		return left.Skill < right.Skill
	})
}
