package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

const (
	minHubCollectInterval = 30
	maxHubCollectInterval = 3600
	maxHubNameBytes       = 256
	maxHubNetworkBytes    = 256
	maxHubURLBytes        = 4096
	maxHubTokenBytes      = 64 << 10
)

func (s *Store) CreateHub(ctx context.Context, input HubWrite) (Hub, error) {
	normalizeHubWrite(&input)
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
	if err := validateHubWrite(input, true); err != nil {
		return Hub{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Hub{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return Hub{}, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO hubs(name,base_url,network,enabled,verify_tls,collect_interval_seconds) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, input.Name, input.BaseURL, input.Network, enabled, verifyTLS, input.CollectIntervalSeconds).Scan(&id)
	if err != nil {
		return Hub{}, err
	}
	if input.APIToken != "" {
		encrypted, err := s.Cipher.Encrypt([]byte(input.APIToken), fmt.Sprintf("hub:%d:token", id))
		if err != nil {
			return Hub{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE hubs SET api_token_encrypted=$2 WHERE id=$1`, id, encrypted); err != nil {
			return Hub{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Hub{}, err
	}
	return s.GetHub(ctx, id)
}

func (s *Store) GetHub(ctx context.Context, id int64) (Hub, error) {
	h, err := scanHub(s.Pool.QueryRow(ctx, `SELECT `+hubColumns+` FROM hubs WHERE id=$1`, id))
	return h, dbNotFound(err)
}

// GetHubCredential reads the Hub endpoint configuration and its bound token in
// one statement. Operational callers must use this method rather than combining
// GetHub with a separate secret lookup, otherwise a concurrent Hub update can
// pair an old endpoint with a new token (or vice versa).
func (s *Store) GetHubCredential(ctx context.Context, id int64) (Hub, string, error) {
	var h Hub
	var snapshot, encrypted []byte
	err := s.Pool.QueryRow(ctx, `SELECT `+hubColumns+`,api_token_encrypted FROM hubs WHERE id=$1`, id).Scan(
		&h.ID, &h.Name, &h.BaseURL, &h.Network, &h.Enabled, &h.VerifyTLS,
		&h.CollectIntervalSeconds, &h.TokenConfigured, &h.Status, &h.Version,
		&h.LastSeenAt, &h.LastError, &snapshot, &h.CreatedAt, &h.UpdatedAt, &encrypted,
	)
	if err != nil {
		return Hub{}, "", dbNotFound(err)
	}
	h.Snapshot = json.RawMessage(snapshot)
	h.CredentialGeneration = hubCredentialGeneration(h.ID, h.BaseURL, h.VerifyTLS, h.Enabled, encrypted)
	if len(encrypted) == 0 {
		return h, "", nil
	}
	plain, err := s.Cipher.Decrypt(encrypted, fmt.Sprintf("hub:%d:token", id))
	if err != nil {
		return Hub{}, "", err
	}
	return h, string(plain), nil
}

// hubCredentialGeneration deliberately fingerprints the encrypted token rather
// than its plaintext. Re-entering a token creates a new generation even if the
// plaintext happens to be the same, which is the conservative behavior for an
// in-flight request. Length-prefixing removes concatenation ambiguity.
func hubCredentialGeneration(id int64, baseURL string, verifyTLS, enabled bool, encrypted []byte) string {
	h := sha256.New()
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(id))
	_, _ = h.Write(number[:])
	writeHubGenerationField(h, []byte(strings.TrimRight(strings.TrimSpace(baseURL), "/")))
	flags := byte(0)
	if verifyTLS {
		flags |= 1
	}
	if enabled {
		flags |= 2
	}
	_, _ = h.Write([]byte{flags})
	writeHubGenerationField(h, encrypted)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type hubGenerationWriter interface {
	Write([]byte) (int, error)
}

func writeHubGenerationField(h hubGenerationWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}

func (s *Store) lockAndMatchHubCredential(ctx context.Context, tx pgx.Tx, hub Hub) (bool, error) {
	var baseURL string
	var verifyTLS, enabled bool
	var encrypted []byte
	if err := tx.QueryRow(ctx, `SELECT base_url,verify_tls,enabled,api_token_encrypted FROM hubs WHERE id=$1 FOR UPDATE`, hub.ID).Scan(&baseURL, &verifyTLS, &enabled, &encrypted); err != nil {
		return false, dbNotFound(err)
	}
	if hub.CredentialGeneration == "" {
		return false, ErrHubCredentialChanged
	}
	current := hubCredentialGeneration(hub.ID, baseURL, verifyTLS, enabled, encrypted)
	return subtle.ConstantTimeCompare([]byte(current), []byte(hub.CredentialGeneration)) == 1, nil
}

func (s *Store) ListHubs(ctx context.Context) ([]Hub, error) {
	return s.ListHubsWithAccess(ctx, AccessFilter{Global: true})
}

func (s *Store) ListHubsWithAccess(ctx context.Context, access AccessFilter) ([]Hub, error) {
	predicate, args := AccessPredicate(access, "id", "", 1)
	rows, err := s.Pool.Query(ctx, `SELECT `+hubColumns+` FROM hubs WHERE `+predicate+` ORDER BY name`, args...)
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Hub{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return Hub{}, err
	}

	// Lock before deriving omitted fields or validating the endpoint/token
	// binding. This makes two concurrent partial updates observe each other in
	// serialization order instead of retaining a token bound to another URL.
	current, err := scanHub(tx.QueryRow(ctx, `SELECT `+hubColumns+` FROM hubs WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return Hub{}, dbNotFound(err)
	}
	normalizeHubWrite(&input)
	if input.Name == "" {
		input.Name = current.Name
	}
	if input.BaseURL == "" {
		input.BaseURL = current.BaseURL
	}
	if input.Network == "" {
		input.Network = current.Network
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
	if err := validateHubWrite(input, false); err != nil {
		return Hub{}, err
	}
	if strings.TrimSpace(input.APIToken) == "" && (!current.TokenConfigured || hubCredentialBindingChanged(current.BaseURL, current.VerifyTLS, input.BaseURL, verifyTLS)) {
		return Hub{}, errors.New("JupyterHub URL 또는 TLS 검증 설정을 바꾸려면 관리자 API token을 다시 입력해야 합니다")
	}
	var encryptedToken []byte
	if input.APIToken != "" {
		encryptedToken, err = s.Cipher.Encrypt([]byte(input.APIToken), fmt.Sprintf("hub:%d:token", id))
		if err != nil {
			return Hub{}, err
		}
	}
	replaceTarget := current.BaseURL != input.BaseURL
	updated, err := scanHub(tx.QueryRow(ctx, `
		UPDATE hubs SET
			name=$2,base_url=$3,network=$4,enabled=$5,verify_tls=$6,
			collect_interval_seconds=$7,
			api_token_encrypted=CASE WHEN $8::bytea IS NULL THEN api_token_encrypted ELSE $8::bytea END,
			status=CASE WHEN $9 THEN 'unknown' ELSE status END,
			version=CASE WHEN $9 THEN '' ELSE version END,
			last_seen_at=CASE WHEN $9 THEN NULL ELSE last_seen_at END,
			last_error=CASE WHEN $9 THEN '' ELSE last_error END,
			snapshot=CASE WHEN $9 THEN '{}'::jsonb ELSE snapshot END,
			updated_at=now()
		WHERE id=$1
		RETURNING `+hubColumns,
		id, input.Name, input.BaseURL, input.Network, enabled, verifyTLS,
		input.CollectIntervalSeconds, encryptedToken, replaceTarget,
	))
	if err != nil {
		return Hub{}, dbNotFound(err)
	}
	// A different base URL is a different authority even when the numeric Hub
	// ID is retained. Remove the old authority's materialized current state so
	// a later server action cannot send an A-derived username to endpoint B.
	// Historical sessions remain available with server_id set to NULL.
	if replaceTarget {
		if _, err := tx.Exec(ctx, `DELETE FROM servers WHERE hub_id=$1`, id); err != nil {
			return Hub{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM managed_users WHERE hub_id=$1`, id); err != nil {
			return Hub{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Hub{}, err
	}
	return updated, nil
}

func normalizeHubWrite(input *HubWrite) {
	input.Name = strings.TrimSpace(input.Name)
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.Network = strings.TrimSpace(input.Network)
	input.APIToken = strings.TrimSpace(input.APIToken)
}

func validateHubWrite(input HubWrite, requireToken bool) error {
	if input.Name == "" {
		return errors.New("name is required")
	}
	if input.Network == "" {
		return errors.New("network is required")
	}
	if input.BaseURL == "" {
		return errors.New("base_url is required")
	}
	if requireToken && input.APIToken == "" {
		return errors.New("api_token is required")
	}
	if len(input.Name) > maxHubNameBytes || len(input.Network) > maxHubNetworkBytes {
		return errors.New("name and network must each be 256 bytes or less")
	}
	if len(input.BaseURL) > maxHubURLBytes {
		return errors.New("base_url must be 4096 bytes or less")
	}
	if len(input.APIToken) > maxHubTokenBytes {
		return errors.New("api_token must be 64KiB or less")
	}
	if input.CollectIntervalSeconds < minHubCollectInterval || input.CollectIntervalSeconds > maxHubCollectInterval {
		return fmt.Errorf("collect_interval_seconds must be between %d and %d", minHubCollectInterval, maxHubCollectInterval)
	}
	if _, err := integration.ValidateEndpoint(input.BaseURL); err != nil {
		return err
	}
	return nil
}

func hubCredentialBindingChanged(currentURL string, currentVerifyTLS bool, nextURL string, nextVerifyTLS bool) bool {
	currentURL = strings.TrimRight(strings.TrimSpace(currentURL), "/")
	nextURL = strings.TrimRight(strings.TrimSpace(nextURL), "/")
	return currentURL != nextURL || currentVerifyTLS != nextVerifyTLS
}

func (s *Store) DeleteHub(ctx context.Context, id int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Scope cleanup and role binding updates share RBAC -> inventory ordering.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, rbacMutationLockKey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `DELETE FROM hubs WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	// Generic scope clauses intentionally avoid a polymorphic foreign key.
	// Remove deleted Hub targets so a manually reused sequence value cannot
	// revive an old authorization binding.
	if _, err := tx.Exec(ctx, `DELETE FROM user_role_scope_clauses WHERE scope_type='hub' AND scope_value=$1`, strconv.FormatInt(id, 10)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateHubHealthIfCurrent persists an operational result only when the Hub
// endpoint and credential are still the exact generation used for the remote
// request. The row lock makes the comparison and write one atomic operation
// with respect to UpdateHub and DeleteHub.
func (s *Store) UpdateHubHealthIfCurrent(ctx context.Context, hub Hub, success bool, version, errorMessage string, snapshot any) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := s.lockAndMatchHubCredential(ctx, tx, hub)
	if err != nil {
		return false, err
	}
	if !current {
		return false, nil
	}
	if success {
		raw := normalizeJSON(snapshot)
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		if _, err := tx.Exec(ctx, `UPDATE hubs SET status='healthy',version=$2,last_seen_at=now(),last_error='',snapshot=$3,updated_at=now() WHERE id=$1`, hub.ID, version, raw); err != nil {
			return false, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE hubs SET status='degraded',last_error=$2,updated_at=now() WHERE id=$1`, hub.ID, truncate(errorMessage, 1000)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// SyncHubUsersIfCurrent applies a remote user/server snapshot only while the
// same Hub credential generation is still current. Holding the Hub row lock
// through the child-table transaction prevents a target replacement from
// racing between the check and the final commit.
func (s *Store) SyncHubUsersIfCurrent(ctx context.Context, hub Hub, users []integration.JupyterUser) (bool, error) {
	err := s.syncHubUsers(ctx, hub, users, true)
	if errors.Is(err, ErrHubCredentialChanged) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) syncHubUsers(ctx context.Context, hub Hub, users []integration.JupyterUser, requireCurrent bool) error {
	plan := planHubSnapshot(users)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return err
	}
	if requireCurrent {
		current, err := s.lockAndMatchHubCredential(ctx, tx, hub)
		if err != nil {
			return err
		}
		if !current {
			return ErrHubCredentialChanged
		}
	}
	existing, err := runningServerStates(ctx, tx, hub.ID)
	if err != nil {
		return err
	}
	observedAt := time.Now().UTC()
	for key, state := range existing {
		if _, active := plan.ActiveServers[key]; active {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE servers SET status='stopped',synced_at=$2 WHERE id=$1`, state.ID, observedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE server_sessions SET ended_at=$2,last_activity_at=COALESCE(last_activity_at,$2) WHERE server_id=$1 AND ended_at IS NULL`, state.ID, observedAt); err != nil {
			return err
		}
	}
	for _, user := range users {
		var managedID int64
		userSnapshot := safeJupyterUserSnapshot(user)
		err := tx.QueryRow(ctx, `INSERT INTO managed_users(hub_id,username,admin,last_activity_at,raw) VALUES($1,$2,$3,$4,$5) ON CONFLICT(hub_id,username) DO UPDATE SET admin=EXCLUDED.admin,last_activity_at=EXCLUDED.last_activity_at,raw=EXCLUDED.raw,active=true,synced_at=now() RETURNING id`, hub.ID, user.Name, user.Admin, user.LastActivity, userSnapshot).Scan(&managedID)
		if err != nil {
			return err
		}
		for name, raw := range user.Servers {
			server := parseJupyterServer(raw)
			serverSnapshot := safeJupyterServerSnapshot(raw)
			if server.Status == "stopped" {
				continue
			}
			var serverID int64
			err = tx.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,started_at,last_activity_at,url,node_name,pod_name,image,cpu_cores,memory_bytes,gpu_count,raw) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT(hub_id,username,server_name) DO UPDATE SET managed_user_id=EXCLUDED.managed_user_id,status=EXCLUDED.status,started_at=COALESCE(EXCLUDED.started_at,servers.started_at),last_activity_at=COALESCE(EXCLUDED.last_activity_at,servers.last_activity_at),url=EXCLUDED.url,node_name=COALESCE(NULLIF(EXCLUDED.node_name,''),servers.node_name),pod_name=COALESCE(NULLIF(EXCLUDED.pod_name,''),servers.pod_name),image=COALESCE(NULLIF(EXCLUDED.image,''),servers.image),cpu_cores=COALESCE(EXCLUDED.cpu_cores,servers.cpu_cores),memory_bytes=COALESCE(EXCLUDED.memory_bytes,servers.memory_bytes),gpu_count=COALESCE(EXCLUDED.gpu_count,servers.gpu_count),raw=servers.raw||EXCLUDED.raw,synced_at=now() RETURNING id`, hub.ID, managedID, user.Name, name, server.Status, server.Started, server.LastActivity, server.URL, server.Node, server.Pod, server.Image, server.CPU, server.Memory, server.GPU, serverSnapshot).Scan(&serverID)
			if err != nil {
				return err
			}
			if err := reconcileServerSession(ctx, tx, hub.ID, serverID, user.Name, name, server.Started, server.LastActivity, existing[user.Name+"\x00"+name], observedAt); err != nil {
				return err
			}
		}
		if len(user.Servers) == 0 && user.Server != "" {
			raw, _ := json.Marshal(map[string]any{"url": user.Server, "ready": true, "last_activity": user.LastActivity})
			var serverID int64
			err = tx.QueryRow(ctx, `INSERT INTO servers(hub_id,managed_user_id,username,server_name,status,last_activity_at,url,raw) VALUES($1,$2,$3,'','running',$4,$5,$6) ON CONFLICT(hub_id,username,server_name) DO UPDATE SET managed_user_id=EXCLUDED.managed_user_id,status='running',last_activity_at=COALESCE(EXCLUDED.last_activity_at,servers.last_activity_at),url=EXCLUDED.url,raw=EXCLUDED.raw,synced_at=now() RETURNING id`, hub.ID, managedID, user.Name, user.LastActivity, user.Server, raw).Scan(&serverID)
			if err != nil {
				return err
			}
			if err := reconcileServerSession(ctx, tx, hub.ID, serverID, user.Name, "", nil, user.LastActivity, existing[user.Name+"\x00"], observedAt); err != nil {
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

type runningServerState struct {
	ID      int64
	Started *time.Time
}

func runningServerStates(ctx context.Context, tx pgx.Tx, hubID int64) (map[string]runningServerState, error) {
	rows, err := tx.Query(ctx, `SELECT id,username,server_name,started_at FROM servers WHERE hub_id=$1 AND status<>'stopped'`, hubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]runningServerState{}
	for rows.Next() {
		var state runningServerState
		var username, serverName string
		if err := rows.Scan(&state.ID, &username, &serverName, &state.Started); err != nil {
			return nil, err
		}
		states[username+"\x00"+serverName] = state
	}
	return states, rows.Err()
}

func reconcileServerSession(ctx context.Context, tx pgx.Tx, hubID, serverID int64, username, serverName string, startedAt, lastActivityAt *time.Time, previous runningServerState, observedAt time.Time) error {
	restarted := previous.ID != 0 && startedAt != nil && (previous.Started == nil || startedAt.After(previous.Started.Add(time.Second)))
	if restarted {
		endedAt := observedAt
		if startedAt.Before(observedAt) {
			endedAt = *startedAt
		}
		if _, err := tx.Exec(ctx, `UPDATE server_sessions SET ended_at=$2,last_activity_at=COALESCE(last_activity_at,$2) WHERE server_id=$1 AND ended_at IS NULL`, serverID, endedAt); err != nil {
			return err
		}
	}
	start := observedAt
	if startedAt != nil && !startedAt.After(observedAt.Add(time.Minute)) {
		start = *startedAt
	} else if previous.Started != nil {
		start = *previous.Started
	}
	_, err := tx.Exec(ctx, `INSERT INTO server_sessions(server_id,hub_id,username,server_name,started_at,last_activity_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (server_id) WHERE server_id IS NOT NULL AND ended_at IS NULL DO UPDATE SET last_activity_at=COALESCE(EXCLUDED.last_activity_at,server_sessions.last_activity_at)`, serverID, hubID, username, serverName, start, lastActivityAt)
	return err
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
		for name, raw := range user.Servers {
			if parseJupyterServer(raw).Status != "stopped" {
				plan.ActiveServers[user.Name+"\x00"+name] = struct{}{}
			}
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

func safeJupyterUserSnapshot(user integration.JupyterUser) []byte {
	snapshot := map[string]any{}
	if len(user.Roles) > 0 {
		snapshot["roles"] = user.Roles
	}
	if len(user.Groups) > 0 {
		snapshot["groups"] = user.Groups
	}
	if user.Pending != nil {
		snapshot["pending"] = *user.Pending
	}
	return normalizeJSON(snapshot)
}

func safeJupyterServerSnapshot(raw json.RawMessage) []byte {
	var source map[string]any
	_ = json.Unmarshal(raw, &source)
	snapshot := map[string]any{}
	for _, key := range []string{"project", "profile", "resource_profile", "pending"} {
		if value, ok := source[key].(string); ok {
			snapshot[key] = value
		}
	}
	if value, ok := source["ready"].(bool); ok {
		snapshot["ready"] = value
	}
	if value, ok := numericJSONValue(source["progress"]); ok && finiteNonNegative(value) && value <= 100 {
		snapshot["progress"] = value
	}
	return normalizeJSON(snapshot)
}

func numericJSONValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
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
	return s.ListManagedUsersWithAccess(ctx, page, pageSize, hubID, search, AccessFilter{Global: true}, Sort{})
}

func (s *Store) ListManagedUsersWithAccess(ctx context.Context, page, pageSize int, hubID int64, search string, access AccessFilter, sort Sort) ([]ManagedUser, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	departmentSQL := "COALESCE(NULLIF(u.department,''),account.department,'')"
	countAccessSQL, countAccessArgs := AccessPredicate(access, "u.hub_id", departmentSQL, 4)
	var total int
	countArgs := append([]any{hubID, search, pattern}, countAccessArgs...)
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM managed_users u LEFT JOIN users account ON lower(account.username)=lower(u.username) WHERE ($1=0 OR u.hub_id=$1) AND ($2='' OR u.username ILIKE $3) AND `+countAccessSQL, countArgs...).Scan(&total); err != nil {
		return nil, Page{}, err
	}
	dataAccessSQL, dataAccessArgs := AccessPredicate(access, "u.hub_id", departmentSQL, 6)
	dataArgs := append([]any{hubID, search, pattern, pageSize, offset}, dataAccessArgs...)
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id,u.hub_id,h.name,u.username,u.display_name,COALESCE(NULLIF(u.department,''),account.department,''),u.admin,u.active,
			CASE
				WHEN u.last_activity_at IS NULL THEN server_summary.last_activity_at
				WHEN server_summary.last_activity_at IS NULL THEN u.last_activity_at
				ELSE GREATEST(u.last_activity_at,server_summary.last_activity_at)
			END,
			u.raw,u.synced_at,server_summary.server_status,server_summary.server_count,
			server_summary.running_server_count,server_summary.runtime_seconds,
			server_summary.cpu_cores,server_summary.memory_bytes
		FROM managed_users u
		JOIN hubs h ON h.id=u.hub_id
		LEFT JOIN users account ON lower(account.username)=lower(u.username)
		LEFT JOIN LATERAL (
			SELECT
				CASE
					WHEN count(*) FILTER (WHERE s.status='running')>0 THEN 'running'
					WHEN count(*) FILTER (WHERE s.status='starting')>0 THEN 'starting'
					WHEN count(*) FILTER (WHERE s.status LIKE 'pending_%')>0 THEN 'pending'
					WHEN count(*) FILTER (WHERE s.status<>'stopped')>0 THEN 'active'
					WHEN count(*)>0 THEN 'stopped'
					ELSE ''
				END AS server_status,
				count(*) FILTER (WHERE s.status<>'stopped') AS server_count,
				count(*) FILTER (WHERE s.status='running') AS running_server_count,
				COALESCE(sum(floor(GREATEST(0,extract(epoch FROM (now()-s.started_at)))))
					FILTER (WHERE s.status='running' AND s.started_at IS NOT NULL),0)::bigint AS runtime_seconds,
				CASE
					WHEN count(*) FILTER (WHERE s.status='running')>0
						AND count(s.cpu_cores) FILTER (WHERE s.status='running')=count(*) FILTER (WHERE s.status='running')
					THEN sum(s.cpu_cores) FILTER (WHERE s.status='running')
				END AS cpu_cores,
				CASE
					WHEN count(*) FILTER (WHERE s.status='running')>0
						AND count(s.memory_bytes) FILTER (WHERE s.status='running')=count(*) FILTER (WHERE s.status='running')
					THEN sum(s.memory_bytes) FILTER (WHERE s.status='running')
				END AS memory_bytes,
				max(s.last_activity_at) AS last_activity_at
			FROM servers s
			WHERE s.hub_id=u.hub_id AND s.username=u.username
		) server_summary ON true
		WHERE ($1=0 OR u.hub_id=$1) AND ($2='' OR u.username ILIKE $3) AND `+dataAccessSQL+`
		ORDER BY `+orderBy(managedUserSortColumns, sort, "u.username,u.hub_id", "u.username,u.hub_id")+` LIMIT $4 OFFSET $5`, dataArgs...)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []ManagedUser{}
	for rows.Next() {
		var u ManagedUser
		var raw []byte
		if err := rows.Scan(&u.ID, &u.HubID, &u.HubName, &u.Username, &u.DisplayName, &u.Department, &u.Admin, &u.Active, &u.LastActivityAt, &raw, &u.SyncedAt, &u.ServerStatus, &u.ServerCount, &u.RunningServerCount, &u.RuntimeSeconds, &u.CPUCores, &u.MemoryBytes); err != nil {
			return nil, Page{}, err
		}
		u.Raw = json.RawMessage(raw)
		u.Roles = managedUserRoles(u.Raw)
		items = append(items, u)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func managedUserRoles(raw json.RawMessage) []string {
	roles := []string{}
	var snapshot struct {
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return roles
	}
	seen := map[string]struct{}{}
	for _, candidate := range snapshot.Roles {
		role := strings.TrimSpace(candidate)
		if role == "" || len(role) > 128 {
			continue
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
		if len(roles) == 64 {
			break
		}
	}
	return roles
}

func (s *Store) ListServers(ctx context.Context, page, pageSize int, hubID int64, status, search string) ([]Server, Page, error) {
	return s.ListServersWithAccess(ctx, page, pageSize, hubID, status, search, AccessFilter{Global: true}, Sort{})
}

func (s *Store) ListServersWithAccess(ctx context.Context, page, pageSize int, hubID int64, status, search string, access AccessFilter, sort Sort) ([]Server, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	departmentSQL := "COALESCE(NULLIF(u.department,''),account.department,'')"
	countAccessSQL, countAccessArgs := AccessPredicate(access, "s.hub_id", departmentSQL, 5)
	var total int
	countArgs := append([]any{hubID, status, search, pattern}, countAccessArgs...)
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM servers s LEFT JOIN managed_users u ON u.id=s.managed_user_id LEFT JOIN users account ON lower(account.username)=lower(s.username) WHERE ($1=0 OR s.hub_id=$1) AND ($2='' OR s.status=$2) AND ($3='' OR s.username ILIKE $4) AND `+countAccessSQL, countArgs...).Scan(&total)
	if err != nil {
		return nil, Page{}, err
	}
	dataAccessSQL, dataAccessArgs := AccessPredicate(access, "s.hub_id", departmentSQL, 7)
	dataArgs := append([]any{hubID, status, search, pattern, pageSize, offset}, dataAccessArgs...)
	rows, err := s.Pool.Query(ctx, `SELECT s.id,s.hub_id,h.name,s.username,COALESCE(NULLIF(u.department,''),account.department,''),s.server_name,s.status,s.started_at,s.last_activity_at,s.url,s.node_name,s.pod_name,s.image,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id LEFT JOIN users account ON lower(account.username)=lower(s.username) WHERE ($1=0 OR s.hub_id=$1) AND ($2='' OR s.status=$2) AND ($3='' OR s.username ILIKE $4) AND `+dataAccessSQL+` ORDER BY `+orderBy(serverSortColumns, sort, "s.synced_at DESC", "s.id")+` LIMIT $5 OFFSET $6`, dataArgs...)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	var features map[string]bool
	_ = s.GetSetting(ctx, "features", &features)
	gpuEnabled := features["gpu_monitoring"]
	items := []Server{}
	for rows.Next() {
		var v Server
		var raw []byte
		if err := rows.Scan(&v.ID, &v.HubID, &v.HubName, &v.Username, &v.Department, &v.ServerName, &v.Status, &v.StartedAt, &v.LastActivityAt, &v.URL, &v.NodeName, &v.PodName, &v.Image, &v.CPUCores, &v.MemoryBytes, &v.GPUCount, &raw, &v.SyncedAt); err != nil {
			return nil, Page{}, err
		}
		v.Raw = json.RawMessage(raw)
		if !gpuEnabled {
			v.GPUCount = nil
		}
		items = append(items, v)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, id int64) (Server, error) {
	var v Server
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT s.id,s.hub_id,h.name,s.username,COALESCE(NULLIF(u.department,''),account.department,''),s.server_name,s.status,s.started_at,s.last_activity_at,s.url,s.node_name,s.pod_name,s.image,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id LEFT JOIN users account ON lower(account.username)=lower(s.username) WHERE s.id=$1`, id).Scan(&v.ID, &v.HubID, &v.HubName, &v.Username, &v.Department, &v.ServerName, &v.Status, &v.StartedAt, &v.LastActivityAt, &v.URL, &v.NodeName, &v.PodName, &v.Image, &v.CPUCores, &v.MemoryBytes, &v.GPUCount, &raw, &v.SyncedAt)
	v.Raw = json.RawMessage(raw)
	return v, dbNotFound(err)
}

// GetServerActionCredential reads the remote server identity and the Hub
// endpoint/token in one MVCC snapshot. This prevents a server action from
// combining an A-derived username with a concurrently replaced B endpoint.
func (s *Store) GetServerActionCredential(ctx context.Context, id int64) (Server, Hub, string, error) {
	var server Server
	var hub Hub
	var encrypted []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT s.id,s.hub_id,s.username,s.server_name,
		       h.id,h.name,h.base_url,h.network,h.enabled,h.verify_tls,
		       h.collect_interval_seconds,(h.api_token_encrypted IS NOT NULL),
		       h.api_token_encrypted
		FROM servers s JOIN hubs h ON h.id=s.hub_id
		WHERE s.id=$1`, id).Scan(
		&server.ID, &server.HubID, &server.Username, &server.ServerName,
		&hub.ID, &hub.Name, &hub.BaseURL, &hub.Network, &hub.Enabled, &hub.VerifyTLS,
		&hub.CollectIntervalSeconds, &hub.TokenConfigured, &encrypted,
	)
	if err != nil {
		return Server{}, Hub{}, "", dbNotFound(err)
	}
	hub.CredentialGeneration = hubCredentialGeneration(hub.ID, hub.BaseURL, hub.VerifyTLS, hub.Enabled, encrypted)
	if len(encrypted) == 0 {
		return server, hub, "", nil
	}
	plain, err := s.Cipher.Decrypt(encrypted, fmt.Sprintf("hub:%d:token", hub.ID))
	if err != nil {
		return Server{}, Hub{}, "", err
	}
	return server, hub, string(plain), nil
}

func truncate(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
