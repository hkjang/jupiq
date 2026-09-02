package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/jackc/pgx/v5"
)

func scanHub(row pgx.Row) (Hub, error) {
	var h Hub
	var snapshot []byte
	err := row.Scan(&h.ID, &h.Name, &h.BaseURL, &h.Network, &h.Enabled, &h.VerifyTLS, &h.CollectIntervalSeconds, &h.TokenConfigured, &h.Status, &h.Version, &h.LastSeenAt, &h.LastError, &snapshot, &h.CreatedAt, &h.UpdatedAt)
	h.Snapshot = json.RawMessage(snapshot)
	return h, err
}

const hubColumns = `id,name,base_url,network,enabled,verify_tls,collect_interval_seconds,(api_token_encrypted IS NOT NULL),status,version,last_seen_at,last_error,snapshot,created_at,updated_at`

func (s *Store) CreateHub(ctx context.Context, input HubWrite) (Hub, error) {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.BaseURL) == "" {
		return Hub{}, errors.New("name and base_url are required")
	}
	if _, err := integration.ValidateEndpoint(input.BaseURL); err != nil {
		return Hub{}, err
	}
	enabled, verifyTLS := true, true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.VerifyTLS != nil {
		verifyTLS = *input.VerifyTLS
	}
	if input.CollectIntervalSeconds == 0 {
		input.CollectIntervalSeconds = 60
	}
	var id int64
	err := s.Pool.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network,enabled,verify_tls,collect_interval_seconds) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, input.Name, strings.TrimRight(input.BaseURL, "/"), input.Network, enabled, verifyTLS, input.CollectIntervalSeconds).Scan(&id)
	if err != nil {
		return Hub{}, err
	}
	if input.APIToken != "" {
		encrypted, err := s.Cipher.Encrypt([]byte(input.APIToken), fmt.Sprintf("hub:%d:token", id))
		if err != nil {
			return Hub{}, err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE hubs SET api_token_encrypted=$2 WHERE id=$1`, id, encrypted); err != nil {
			return Hub{}, err
		}
	}
	return s.GetHub(ctx, id)
}

func (s *Store) GetHub(ctx context.Context, id int64) (Hub, error) {
	h, err := scanHub(s.Pool.QueryRow(ctx, `SELECT `+hubColumns+` FROM hubs WHERE id=$1`, id))
	return h, dbNotFound(err)
}

func (s *Store) HubToken(ctx context.Context, id int64) (string, error) {
	var encrypted []byte
	err := s.Pool.QueryRow(ctx, `SELECT api_token_encrypted FROM hubs WHERE id=$1`, id).Scan(&encrypted)
	if err != nil {
		return "", dbNotFound(err)
	}
	if len(encrypted) == 0 {
		return "", nil
	}
	plain, err := s.Cipher.Decrypt(encrypted, fmt.Sprintf("hub:%d:token", id))
	return string(plain), err
}

func (s *Store) ListHubs(ctx context.Context) ([]Hub, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+hubColumns+` FROM hubs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hubs := []Hub{}
	for rows.Next() {
		h, err := scanHub(rows)
		if err != nil {
			return nil, err
		}
		hubs = append(hubs, h)
	}
	return hubs, rows.Err()
}

func (s *Store) UpdateHub(ctx context.Context, id int64, input HubWrite) (Hub, error) {
	current, err := s.GetHub(ctx, id)
	if err != nil {
		return Hub{}, err
	}
	if input.Name == "" {
		input.Name = current.Name
	}
	if input.BaseURL == "" {
		input.BaseURL = current.BaseURL
	}
	if _, err := integration.ValidateEndpoint(input.BaseURL); err != nil {
		return Hub{}, err
	}
	enabled, verifyTLS := current.Enabled, current.VerifyTLS
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.VerifyTLS != nil {
		verifyTLS = *input.VerifyTLS
	}
	if input.CollectIntervalSeconds == 0 {
		input.CollectIntervalSeconds = current.CollectIntervalSeconds
	}
	result, err := s.Pool.Exec(ctx, `UPDATE hubs SET name=$2,base_url=$3,network=$4,enabled=$5,verify_tls=$6,collect_interval_seconds=$7,updated_at=now() WHERE id=$1`, id, input.Name, strings.TrimRight(input.BaseURL, "/"), input.Network, enabled, verifyTLS, input.CollectIntervalSeconds)
	if err != nil {
		return Hub{}, err
	}
	if result.RowsAffected() == 0 {
		return Hub{}, ErrNotFound
	}
	if input.APIToken != "" {
		encrypted, err := s.Cipher.Encrypt([]byte(input.APIToken), fmt.Sprintf("hub:%d:token", id))
		if err != nil {
			return Hub{}, err
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE hubs SET api_token_encrypted=$2 WHERE id=$1`, id, encrypted); err != nil {
			return Hub{}, err
		}
	}
	return s.GetHub(ctx, id)
}

func (s *Store) DeleteHub(ctx context.Context, id int64) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateHubHealth(ctx context.Context, id int64, success bool, version, errorMessage string, snapshot any) error {
	if success {
		raw := normalizeJSON(snapshot)
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		_, err := s.Pool.Exec(ctx, `UPDATE hubs SET status='healthy',version=$2,last_seen_at=now(),last_error='',snapshot=$3,updated_at=now() WHERE id=$1`, id, version, raw)
		return err
	}
	// Deliberately keep the previous snapshot and last_seen_at on collection errors.
	_, err := s.Pool.Exec(ctx, `UPDATE hubs SET status='degraded',last_error=$2,updated_at=now() WHERE id=$1`, id, truncate(errorMessage, 1000))
	return err
}

func (s *Store) SyncHubUsers(ctx context.Context, hub Hub, users []integration.JupyterUser) error {
	plan := planHubSnapshot(users)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A successful full snapshot is authoritative for liveness. Historical rows
	// remain available, but servers missing from this snapshot become stopped.
	if _, err := tx.Exec(ctx, `UPDATE servers SET status='stopped',synced_at=now() WHERE hub_id=$1 AND status<>'stopped'`, hub.ID); err != nil {
		return err
	}
	for _, user := range users {
		var managedID int64
		err := tx.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,admin,last_activity_at,raw) VALUES($1,$2,$3,$4,$5) ON CONFLICT(hub_id,username) DO UPDATE SET admin=EXCLUDED.admin,last_activity_at=EXCLUDED.last_activity_at,raw=EXCLUDED.raw,active=true,synced_at=now() RETURNING id`, hub.ID, user.Name, user.Admin, user.LastActivity, user.Raw).Scan(&managedID)
		if err != nil {
			return err
		}
		for name, raw := range user.Servers {
			server := parseJupyterServer(raw)
			if server.Status == "stopped" {
				continue
			}
			_, err = tx.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,url,node_name,pod_name,image,cpu_cores,memory_bytes,gpu_count,raw) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT(hub_id,username,server_name) DO UPDATE SET managed_user_id=EXCLUDED.managed_user_id,status=EXCLUDED.status,started_at=EXCLUDED.started_at,last_activity_at=EXCLUDED.last_activity_at,url=EXCLUDED.url,node_name=COALESCE(NULLIF(EXCLUDED.node_name,''),servers.node_name),pod_name=COALESCE(NULLIF(EXCLUDED.pod_name,''),servers.pod_name),image=COALESCE(NULLIF(EXCLUDED.image,''),servers.image),cpu_cores=COALESCE(EXCLUDED.cpu_cores,servers.cpu_cores),memory_bytes=COALESCE(EXCLUDED.memory_bytes,servers.memory_bytes),gpu_count=COALESCE(EXCLUDED.gpu_count,servers.gpu_count),raw=servers.raw||EXCLUDED.raw,synced_at=now()`, hub.ID, managedID, user.Name, name, server.Status, server.Started, server.LastActivity, server.URL, server.Node, server.Pod, server.Image, server.CPU, server.Memory, server.GPU, raw)
			if err != nil {
				return err
			}
		}
		if len(user.Servers) == 0 && user.Server != "" {
			raw, _ := json.Marshal(map[string]any{"url": user.Server, "ready": true, "last_activity": user.LastActivity})
			_, err = tx.Exec(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,last_activity_at,url,raw) VALUES($1,$2,$3,'','running',$4,$5,$6) ON CONFLICT(hub_id,username,server_name) DO UPDATE SET managed_user_id=EXCLUDED.managed_user_id,status='running',last_activity_at=EXCLUDED.last_activity_at,url=EXCLUDED.url,raw=EXCLUDED.raw,synced_at=now()`, hub.ID, managedID, user.Name, user.LastActivity, user.Server, raw)
			if err != nil {
				return err
			}
		}
	}
	if !plan.DeactivateAllUsers {
		if _, err := tx.Exec(ctx, `UPDATE managed_users SET active=false,synced_at=now() WHERE hub_id=$1 AND NOT(username=ANY($2))`, hub.ID, plan.SeenUsers); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE managed_users SET active=false,synced_at=now() WHERE hub_id=$1`, hub.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type hubSnapshotPlan struct {
	SeenUsers          []string
	ActiveServers      map[string]struct{}
	DeactivateAllUsers bool
}

func planHubSnapshot(users []integration.JupyterUser) hubSnapshotPlan {
	plan := hubSnapshotPlan{SeenUsers: make([]string, 0, len(users)), ActiveServers: map[string]struct{}{}, DeactivateAllUsers: len(users) == 0}
	for _, user := range users {
		plan.SeenUsers = append(plan.SeenUsers, user.Name)
		for name := range user.Servers {
			plan.ActiveServers[user.Name+"\x00"+name] = struct{}{}
		}
		if len(user.Servers) == 0 && user.Server != "" {
			plan.ActiveServers[user.Name+"\x00"] = struct{}{}
		}
	}
	return plan
}

type parsedServer struct {
	Status                string
	Started               *time.Time
	LastActivity          *time.Time
	URL, Node, Pod, Image string
	CPU                   *float64
	Memory                *int64
	GPU                   *int
}

func parseJupyterServer(raw json.RawMessage) parsedServer {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	p := parsedServer{Status: "running"}
	if pending, ok := value["pending"].(string); ok && pending != "" {
		p.Status = "pending_" + pending
	}
	if ready, ok := value["ready"].(bool); ok && !ready && p.Status == "running" {
		p.Status = "starting"
	}
	p.Started = parseTime(value["started"])
	p.LastActivity = parseTime(value["last_activity"])
	p.URL, _ = value["url"].(string)
	for key, target := range map[string]*string{"node_name": &p.Node, "pod_name": &p.Pod, "image": &p.Image} {
		if v, ok := value[key].(string); ok {
			*target = v
		}
	}
	return p
}

func parseTime(value any) *time.Time {
	s, ok := value.(string)
	if !ok || s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

func (s *Store) ListManagedUsers(ctx context.Context, page, pageSize int, hubID int64, search string) ([]ManagedUser, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_users WHERE ($1=0 OR hub_id=$1) AND ($2='' OR username ILIKE $3)`, hubID, search, pattern).Scan(&total); err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT u.id,u.hub_id,h.name,u.username,u.display_name,u.department,u.admin,u.active,u.last_activity_at,u.raw,u.synced_at FROM managed_users u JOIN hubs h ON h.id=u.hub_id WHERE ($1=0 OR u.hub_id=$1) AND ($2='' OR u.username ILIKE $3) ORDER BY u.username LIMIT $4 OFFSET $5`, hubID, search, pattern, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		var raw []byte
		if err := rows.Scan(&u.ID, &u.HubID, &u.HubName, &u.Username, &u.DisplayName, &u.Department, &u.Admin, &u.Active, &u.LastActivityAt, &raw, &u.SyncedAt); err != nil {
			return nil, Page{}, err
		}
		u.Raw = json.RawMessage(raw)
		items = append(items, u)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) ListServers(ctx context.Context, page, pageSize int, hubID int64, status, search string) ([]Server, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	var total int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM servers WHERE ($1=0 OR hub_id=$1) AND ($2='' OR status=$2) AND ($3='' OR username ILIKE $4)`, hubID, status, search, pattern).Scan(&total)
	if err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT s.id,s.hub_id,h.name,s.username,s.server_name,s.status,s.started_at,s.last_activity_at,s.url,s.node_name,s.pod_name,s.image,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE ($1=0 OR s.hub_id=$1) AND ($2='' OR s.status=$2) AND ($3='' OR s.username ILIKE $4) ORDER BY s.synced_at DESC LIMIT $5 OFFSET $6`, hubID, status, search, pattern, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []Server{}
	for rows.Next() {
		var v Server
		var raw []byte
		if err := rows.Scan(&v.ID, &v.HubID, &v.HubName, &v.Username, &v.ServerName, &v.Status, &v.StartedAt, &v.LastActivityAt, &v.URL, &v.NodeName, &v.PodName, &v.Image, &v.CPUCores, &v.MemoryBytes, &v.GPUCount, &raw, &v.SyncedAt); err != nil {
			return nil, Page{}, err
		}
		v.Raw = json.RawMessage(raw)
		items = append(items, v)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, id int64) (Server, error) {
	var v Server
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.id,s.hub_id,h.name,s.username,s.server_name,s.status,s.started_at,s.last_activity_at,s.url,s.node_name,s.pod_name,s.image,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.id=$1`, id).Scan(&v.ID, &v.HubID, &v.HubName, &v.Username, &v.ServerName, &v.Status, &v.StartedAt, &v.LastActivityAt, &v.URL, &v.NodeName, &v.PodName, &v.Image, &v.CPUCores, &v.MemoryBytes, &v.GPUCount, &raw, &v.SyncedAt)
	v.Raw = json.RawMessage(raw)
	return v, dbNotFound(err)
}

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
