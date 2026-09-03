package store

import (
	"fmt"
	"strconv"
	"strings"
)

// AccessPredicate returns a SQL predicate and its positional arguments. The
// column expressions are trusted constants supplied by Store methods, never
// request input. Empty or unrepresentable restricted scopes fail closed.
func AccessPredicate(filter AccessFilter, hubColumn, departmentColumn string, firstArg int) (string, []any) {
	if filter.Global {
		return "TRUE", nil
	}
	parts := make([]string, 0, len(filter.Groups))
	args := make([]any, 0, len(filter.Groups)*2)
	next := firstArg
	for _, group := range filter.Groups {
		if (len(group.HubIDs) > 0 && hubColumn == "") || (len(group.Departments) > 0 && departmentColumn == "") {
			continue
		}
		departments := make([]string, 0, len(group.Departments))
		for _, department := range group.Departments {
			department = strings.ToLower(strings.TrimSpace(department))
			if department != "" {
				departments = append(departments, department)
			}
		}
		if len(group.Departments) > 0 && len(departments) == 0 {
			continue
		}
		conditions := make([]string, 0, 2)
		if len(group.HubIDs) > 0 {
			conditions = append(conditions, fmt.Sprintf("%s=ANY($%d::bigint[])", hubColumn, next))
			args = append(args, group.HubIDs)
			next++
		}
		if len(group.Departments) > 0 {
			conditions = append(conditions, fmt.Sprintf("lower(btrim(COALESCE(%s,'')))=ANY($%d::text[])", departmentColumn, next))
			args = append(args, departments)
			next++
		}
		// A restricted binding without a valid clause is deny-all.
		if len(conditions) > 0 {
			parts = append(parts, "("+strings.Join(conditions, " AND ")+")")
		}
	}
	if len(parts) == 0 {
		return "FALSE", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func ParseHubScope(value string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id, err == nil && id > 0
}
