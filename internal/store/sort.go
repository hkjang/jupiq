package store

import "strings"

// Sort carries a list ordering request from the API layer. Key is an API-level
// column name chosen by the client; Direction is "asc" or "desc".
type Sort struct {
	Key       string
	Direction string
}

// Descending reports whether the caller asked for a descending order.
func (s Sort) Descending() bool { return strings.EqualFold(strings.TrimSpace(s.Direction), "desc") }

// orderBy turns a sort request into a SQL ORDER BY body. The SQL text is taken
// only from `columns`, so a request value can never reach the query - an
// unknown key falls back to the list's natural order. NULLS LAST is set for
// both directions so a row missing the sorted value is always last, matching
// how the table sorts on screen. The tiebreaker keeps paging deterministic when
// the sorted column repeats.
func orderBy(columns map[string]string, sort Sort, fallback, tiebreaker string) string {
	expr, ok := columns[strings.TrimSpace(sort.Key)]
	if !ok || expr == "" {
		return fallback
	}
	clause := expr
	if sort.Descending() {
		clause += " DESC NULLS LAST"
	} else {
		clause += " ASC NULLS LAST"
	}
	if tiebreaker != "" {
		clause += "," + tiebreaker
	}
	return clause
}

// Sortable columns per list endpoint. Keys are what the frontend sends.
var (
	managedUserSortColumns = map[string]string{
		"username":         "u.username",
		"display_name":     "u.display_name",
		"department":       "COALESCE(NULLIF(u.department,''),account.department,'')",
		"hub_name":         "h.name",
		"server_status":    "server_summary.server_status",
		"running_servers":  "server_summary.running_server_count",
		"runtime_seconds":  "server_summary.runtime_seconds",
		"cpu_cores":        "server_summary.cpu_cores",
		"memory_bytes":     "server_summary.memory_bytes",
		"last_activity_at": "server_summary.last_activity_at",
	}
	serverSortColumns = map[string]string{
		"username":         "s.username",
		"hub_name":         "h.name",
		"status":           "s.status",
		"server_name":      "s.server_name",
		"image":            "s.image",
		"node_name":        "s.node_name",
		"pod_name":         "s.pod_name",
		"cpu_cores":        "s.cpu_cores",
		"memory_bytes":     "s.memory_bytes",
		"gpu_count":        "s.gpu_count",
		"started_at":       "s.started_at",
		"last_activity_at": "s.last_activity_at",
		"synced_at":        "s.synced_at",
	}
	resourceSortColumns = map[string]string{
		"name":       "name",
		"status":     "status",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}
	auditSortColumns = map[string]string{
		"created_at":     "created_at",
		"actor_username": "actor_username",
		"action":         "action",
		"resource_type":  "resource_type",
		"resource_id":    "resource_id",
		"ip_address":     "ip_address",
		"result":         "result",
	}
)
