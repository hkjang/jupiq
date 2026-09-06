package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrSettingsSecretReentry = errors.New("the credential must be re-entered when its integration binding changes")
	ErrOIDCSecretRequired    = errors.New("an enabled OIDC integration requires a client secret")
)

const settingsMutationLockKey int64 = 0x6a75706971736574 // "jupiqset"

// integrationInventoryLockKey serializes changes to Hub/server attribution
// identity, including Hub creation (which row locks alone cannot protect from
// as a phantom). Multi-lock operations always acquire settings or RBAC first,
// then this inventory lock.
const integrationInventoryLockKey int64 = 0x6a75706971494e56 // "jupiqINV"

var allowedSettingsSecretKeys = map[string]struct{}{
	"oidc.client_secret": {},
	"prometheus.token":   {},
	"kubernetes.token":   {},
	"ai.api_key":         {},
	"webhook.secret":     {},
}

// IsSettingsSecretKey is the single allowlist used by both the HTTP boundary
// and the persistence layer. Settings must never be able to manufacture an
// arbitrary secret namespace merely by using a password-like field name.
func IsSettingsSecretKey(key string) bool {
	_, ok := allowedSettingsSecretKeys[key]
	return ok
}

func (s *Store) ListSettings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, `SELECT setting_key,value FROM settings ORDER BY setting_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = json.RawMessage(value)
	}
	return settings, rows.Err()
}

func (s *Store) GetSetting(ctx context.Context, key string, target any) error {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE setting_key=$1`, key).Scan(&raw)
	if err != nil {
		return dbNotFound(err)
	}
	return json.Unmarshal(raw, target)
}

func (s *Store) SetSetting(ctx context.Context, key string, value any, userID int64) error {
	return s.UpdateSettingsAndSecrets(ctx, map[string]any{key: value}, nil, userID)
}

func (s *Store) SetSecret(ctx context.Context, key, value string, userID int64) error {
	return s.UpdateSettingsAndSecrets(ctx, nil, map[string]string{key: value}, userID)
}

// UpdateSettingsAndSecrets applies one administrator form submission in a
// single transaction. Encryption is completed before opening the transaction,
// so neither a serialization error nor an encryption error can leave a partial
// configuration behind.
func (s *Store) UpdateSettingsAndSecrets(ctx context.Context, values map[string]any, secrets map[string]string, userID int64) error {
	encodedValues := make(map[string][]byte, len(values))
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if key == "ai" {
			var ai map[string]any
			if err := json.Unmarshal(raw, &ai); err != nil || ai == nil {
				return fmt.Errorf("ai settings must be a JSON object")
			}
			ai["provider"] = "openai-compatible"
			ai["streaming"] = true
			raw, err = json.Marshal(ai)
			if err != nil {
				return err
			}
		}
		encodedValues[key] = raw
	}
	encryptedSecrets := make(map[string][]byte, len(secrets))
	for key, value := range secrets {
		if !IsSettingsSecretKey(key) {
			return fmt.Errorf("unsupported settings secret key %q", key)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("settings secret %q must not be empty", key)
		}
		encrypted, err := s.Cipher.Encrypt([]byte(value), "secret:"+key)
		if err != nil {
			return err
		}
		encryptedSecrets[key] = encrypted
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The lock serializes every settings form mutation before its binding read.
	// READ COMMITTED then observes the last committed config/secret pair and the
	// validation remains true through this transaction's writes and commit.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, settingsMutationLockKey); err != nil {
		return err
	}
	if err := validateStoredSecretBindings(ctx, tx, encodedValues, encryptedSecrets); err != nil {
		return err
	}
	for key, raw := range encodedValues {
		if _, err := tx.Exec(ctx, `INSERT INTO settings(setting_key,value,updated_by) VALUES($1,$2,$3) ON CONFLICT(setting_key) DO UPDATE SET value=EXCLUDED.value,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, raw, userID); err != nil {
			return err
		}
	}
	for key, encrypted := range encryptedSecrets {
		if _, err := tx.Exec(ctx, `INSERT INTO secrets(secret_key,encrypted_value,updated_by) VALUES($1,$2,$3) ON CONFLICT(secret_key) DO UPDATE SET encrypted_value=EXCLUDED.encrypted_value,version=secrets.version+1,updated_by=EXCLUDED.updated_by,updated_at=now()`, key, encrypted, userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type settingsSecretBinding struct {
	kind      string
	section   string
	secretKey string
}

var settingsSecretBindings = []settingsSecretBinding{
	{kind: "oidc", section: "auth.oidc", secretKey: "oidc.client_secret"},
	{kind: "prometheus", section: "prometheus", secretKey: "prometheus.token"},
	{kind: "kubernetes", section: "kubernetes", secretKey: "kubernetes.token"},
	{kind: "ai", section: "ai", secretKey: "ai.api_key"},
	{kind: "webhook", section: "notifications", secretKey: "webhook.secret"},
}

func validateStoredSecretBindings(ctx context.Context, tx pgx.Tx, values, submittedSecrets map[string][]byte) error {
	for _, binding := range settingsSecretBindings {
		candidateRaw, updating := values[binding.section]
		if !updating || len(submittedSecrets[binding.secretKey]) > 0 {
			continue
		}
		var savedRaw []byte
		var secretConfigured bool
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT value FROM settings WHERE setting_key=$1), 'null'::jsonb),
			       EXISTS(SELECT 1 FROM secrets WHERE secret_key=$2)`,
			binding.section, binding.secretKey,
		).Scan(&savedRaw, &secretConfigured); err != nil {
			return err
		}
		var candidate map[string]any
		if err := json.Unmarshal(candidateRaw, &candidate); err != nil {
			return err
		}
		if binding.kind == "oidc" && settingBool(candidate, "enabled") && !secretConfigured {
			return ErrOIDCSecretRequired
		}
		if !secretConfigured {
			continue
		}
		var saved map[string]any
		if string(savedRaw) == "null" || json.Unmarshal(savedRaw, &saved) != nil || !storedIntegrationBindingMatches(binding.kind, candidate, saved) {
			return ErrSettingsSecretReentry
		}
	}
	return nil
}

func settingBool(value map[string]any, key string) bool {
	result, _ := value[key].(bool)
	return result
}

func storedIntegrationBindingMatches(kind string, candidate, saved map[string]any) bool {
	targetKey := "base_url"
	if kind == "oidc" {
		targetKey = "issuer_url"
	}
	candidateTarget, _ := candidate[targetKey].(string)
	savedTarget, _ := saved[targetKey].(string)
	candidateTarget = strings.TrimRight(strings.TrimSpace(candidateTarget), "/")
	savedTarget = strings.TrimRight(strings.TrimSpace(savedTarget), "/")
	if candidateTarget == "" || savedTarget == "" || candidateTarget != savedTarget {
		return false
	}
	if kind == "oidc" {
		candidateClientID, _ := candidate["client_id"].(string)
		savedClientID, _ := saved["client_id"].(string)
		if strings.TrimSpace(candidateClientID) == "" || strings.TrimSpace(candidateClientID) != strings.TrimSpace(savedClientID) {
			return false
		}
	}
	return storedIntegrationVerifyTLS(candidate) == storedIntegrationVerifyTLS(saved)
}

func storedIntegrationVerifyTLS(config map[string]any) bool {
	if value, ok := config["verify_tls"].(bool); ok {
		return value
	}
	return true
}

// GetSettingsAndSecret returns all requested settings and the decrypted secret
// from one SQL statement. PostgreSQL therefore supplies one MVCC snapshot: a
// runtime caller can never combine a new target with the previous credential.
// Missing settings are omitted and a missing secret is reported as configured=false.
func (s *Store) GetSettingsAndSecret(ctx context.Context, settingKeys []string, secretKey string) (map[string]json.RawMessage, string, bool, error) {
	if len(settingKeys) == 0 {
		return map[string]json.RawMessage{}, "", false, nil
	}
	if !IsSettingsSecretKey(secretKey) {
		return nil, "", false, fmt.Errorf("unsupported settings secret key %q", secretKey)
	}
	var settingsRaw []byte
	var encrypted []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(jsonb_object_agg(setting_key,value), '{}'::jsonb),
		       (SELECT encrypted_value FROM secrets WHERE secret_key=$2)
		FROM settings
		WHERE setting_key=ANY($1::text[])`, settingKeys, secretKey).Scan(&settingsRaw, &encrypted)
	if err != nil {
		return nil, "", false, err
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return nil, "", false, err
	}
	if len(encrypted) == 0 {
		return settings, "", false, nil
	}
	plain, err := s.Cipher.Decrypt(encrypted, "secret:"+secretKey)
	if err != nil {
		return nil, "", false, err
	}
	return settings, string(plain), true, nil
}

// GetSettingAndSecret is the typed single-setting form used by most runtime
// integrations. The setting and secret are guaranteed to share one snapshot.
func (s *Store) GetSettingAndSecret(ctx context.Context, settingKey, secretKey string, target any) (string, bool, error) {
	settings, secret, configured, err := s.GetSettingsAndSecret(ctx, []string{settingKey}, secretKey)
	if err != nil {
		return "", false, err
	}
	raw, ok := settings[settingKey]
	if !ok {
		return "", false, ErrNotFound
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return "", false, err
	}
	return secret, configured, nil
}

// IntegrationGeneration is an opaque binding between a provider's settings,
// encrypted credential, and the Hub/server identity inventory observed before
// an outbound collector request. Callers may only obtain one from the Store and
// must present it when persisting the corresponding remote response.
type IntegrationGeneration struct {
	settingKeys          []string
	secretKey            string
	configFingerprint    string
	inventoryFingerprint string
}

// GetSettingsAndSecretGeneration captures provider configuration and the
// attribution inventory from one repeatable-read snapshot. The credential is
// decrypted only after the read-only transaction has committed.
func (s *Store) GetSettingsAndSecretGeneration(ctx context.Context, settingKeys []string, secretKey string) (map[string]json.RawMessage, string, bool, IntegrationGeneration, error) {
	keys := canonicalSettingKeys(settingKeys)
	if len(keys) == 0 {
		return map[string]json.RawMessage{}, "", false, IntegrationGeneration{}, nil
	}
	if !IsSettingsSecretKey(secretKey) {
		return nil, "", false, IntegrationGeneration{}, fmt.Errorf("unsupported settings secret key %q", secretKey)
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, "", false, IntegrationGeneration{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	settings, encrypted, err := settingsAndEncryptedSecret(ctx, tx, keys, secretKey)
	if err != nil {
		return nil, "", false, IntegrationGeneration{}, err
	}
	inventory, err := integrationInventoryFingerprint(ctx, tx)
	if err != nil {
		return nil, "", false, IntegrationGeneration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", false, IntegrationGeneration{}, err
	}
	generation := IntegrationGeneration{
		settingKeys: keys, secretKey: secretKey,
		configFingerprint:    integrationConfigFingerprint(keys, settings, encrypted),
		inventoryFingerprint: inventory,
	}
	if len(encrypted) == 0 {
		return settings, "", false, generation, nil
	}
	plain, err := s.Cipher.Decrypt(encrypted, "secret:"+secretKey)
	if err != nil {
		return nil, "", false, IntegrationGeneration{}, err
	}
	return settings, string(plain), true, generation, nil
}

func (s *Store) GetSettingAndSecretGeneration(ctx context.Context, settingKey, secretKey string, target any) (string, bool, IntegrationGeneration, error) {
	settings, secret, configured, generation, err := s.GetSettingsAndSecretGeneration(ctx, []string{settingKey}, secretKey)
	if err != nil {
		return "", false, IntegrationGeneration{}, err
	}
	raw, ok := settings[settingKey]
	if !ok {
		return "", false, IntegrationGeneration{}, ErrNotFound
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return "", false, IntegrationGeneration{}, err
	}
	return secret, configured, generation, nil
}

type settingsGenerationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func canonicalSettingKeys(settingKeys []string) []string {
	seen := make(map[string]struct{}, len(settingKeys))
	keys := make([]string, 0, len(settingKeys))
	for _, key := range settingKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func settingsAndEncryptedSecret(ctx context.Context, query settingsGenerationQuerier, keys []string, secretKey string) (map[string]json.RawMessage, []byte, error) {
	var settingsRaw, encrypted []byte
	err := query.QueryRow(ctx, `
		SELECT COALESCE(jsonb_object_agg(setting_key,value), '{}'::jsonb),
		       (SELECT encrypted_value FROM secrets WHERE secret_key=$2)
		FROM settings WHERE setting_key=ANY($1::text[])`, keys, secretKey).Scan(&settingsRaw, &encrypted)
	if err != nil {
		return nil, nil, err
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		return nil, nil, err
	}
	return settings, encrypted, nil
}

func integrationConfigFingerprint(keys []string, settings map[string]json.RawMessage, encrypted []byte) string {
	hash := sha256.New()
	for _, key := range keys {
		writeHubGenerationField(hash, []byte(key))
		raw, exists := settings[key]
		if exists {
			_, _ = hash.Write([]byte{1})
			writeHubGenerationField(hash, raw)
		} else {
			_, _ = hash.Write([]byte{0})
		}
	}
	writeHubGenerationField(hash, encrypted)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// integrationInventoryFingerprint includes only fields that control remote
// attribution. Runtime resource measurements are deliberately excluded so a
// preceding metric write does not invalidate the remaining queries in a batch.
func integrationInventoryFingerprint(ctx context.Context, query settingsGenerationQuerier) (string, error) {
	rows, err := query.Query(ctx, `
		SELECT h.id,h.name,h.base_url,h.network,h.enabled,h.verify_tls,h.api_token_encrypted,
		       s.id,s.username,s.server_name,s.status,s.pod_name,COALESCE(u.department,''),COALESCE(s.raw->>'project','')
		FROM hubs h
		LEFT JOIN servers s ON s.hub_id=h.id
		LEFT JOIN managed_users u ON u.id=s.managed_user_id
		ORDER BY h.id,s.id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var hubID int64
		var hubName, baseURL, network string
		var enabled, verifyTLS bool
		var encrypted []byte
		var serverID *int64
		var username, serverName, status, podName, department, project *string
		if err := rows.Scan(&hubID, &hubName, &baseURL, &network, &enabled, &verifyTLS, &encrypted, &serverID, &username, &serverName, &status, &podName, &department, &project); err != nil {
			return "", err
		}
		writeHubGenerationField(hash, []byte(fmt.Sprint(hubID)))
		for _, value := range []string{hubName, baseURL, network, fmt.Sprint(enabled), fmt.Sprint(verifyTLS)} {
			writeHubGenerationField(hash, []byte(value))
		}
		writeHubGenerationField(hash, encrypted)
		if serverID == nil {
			_, _ = hash.Write([]byte{0})
			continue
		}
		_, _ = hash.Write([]byte{1})
		for _, value := range []string{fmt.Sprint(*serverID), derefString(username), derefString(serverName), derefString(status), derefString(podName), derefString(department), derefString(project)} {
			writeHubGenerationField(hash, []byte(value))
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// lockAndMatchIntegrationGeneration serializes provider settings updates and
// all Hub/server attribution changes until the corresponding remote result has
// committed. A false result means the response belongs to an obsolete snapshot.
func lockAndMatchIntegrationGeneration(ctx context.Context, tx pgx.Tx, expected IntegrationGeneration) (bool, error) {
	if expected.configFingerprint == "" || expected.inventoryFingerprint == "" || len(expected.settingKeys) == 0 || expected.secretKey == "" {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, settingsMutationLockKey); err != nil {
		return false, err
	}
	settings, encrypted, err := settingsAndEncryptedSecret(ctx, tx, expected.settingKeys, expected.secretKey)
	if err != nil {
		return false, err
	}
	currentConfig := integrationConfigFingerprint(expected.settingKeys, settings, encrypted)
	if subtle.ConstantTimeCompare([]byte(currentConfig), []byte(expected.configFingerprint)) != 1 {
		return false, nil
	}
	// A dedicated advisory lock also blocks Hub/server inserts, which row locks
	// cannot represent before the new row exists.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, integrationInventoryLockKey); err != nil {
		return false, err
	}
	currentInventory, err := integrationInventoryFingerprint(ctx, tx)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(currentInventory), []byte(expected.inventoryFingerprint)) == 1, nil
}

func (s *Store) GetSecret(ctx context.Context, key string) (string, error) {
	var encrypted []byte
	err := s.Pool.QueryRow(ctx, `SELECT encrypted_value FROM secrets WHERE secret_key=$1`, key).Scan(&encrypted)
	if err != nil {
		return "", dbNotFound(err)
	}
	plain, err := s.Cipher.Decrypt(encrypted, "secret:"+key)
	return string(plain), err
}

func (s *Store) SecretStatus(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT secret_key,version,updated_at FROM secrets ORDER BY secret_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]map[string]any{}
	for rows.Next() {
		var key string
		var version int
		var updated time.Time
		if err := rows.Scan(&key, &version, &updated); err != nil {
			return nil, err
		}
		result[key] = map[string]any{"configured": true, "version": version, "updated_at": updated}
	}
	return result, rows.Err()
}

func (s *Store) ApprovalEnabled(ctx context.Context) bool {
	cfg, err := s.GetWorkflowConfig(ctx)
	return err == nil && cfg.ApprovalEnabled
}

func (s *Store) CreateResource(ctx context.Context, kind string, input ResourceWrite, actorID int64) (Resource, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	var r Resource
	err := s.Pool.QueryRow(ctx, `INSERT INTO resources(kind,name,status,owner_user_id,data,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$6) RETURNING id,created_at,updated_at`, kind, input.Name, input.Status, input.OwnerUserID, input.Data, actorID).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	r.Kind, r.Name, r.Status, r.OwnerUserID, r.Data = kind, input.Name, input.Status, input.OwnerUserID, input.Data
	r.CreatedBy, r.UpdatedBy = &actorID, &actorID
	return r, nil
}

func (s *Store) GetResource(ctx context.Context, kind string, id int64) (Resource, error) {
	var r Resource
	var data []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,kind,name,status,owner_user_id,data,created_by,updated_by,created_at,updated_at FROM resources WHERE kind=$1 AND id=$2`, kind, id).Scan(&r.ID, &r.Kind, &r.Name, &r.Status, &r.OwnerUserID, &data, &r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt)
	r.Data = json.RawMessage(data)
	return r, dbNotFound(err)
}

func (s *Store) ListResources(ctx context.Context, kind string, page, pageSize int, status, search string, ownerID *int64, sort Sort) ([]Resource, Page, error) {
	page, pageSize, offset := pageBounds(page, pageSize)
	pattern := "%" + search + "%"
	var total int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM resources WHERE kind=$1 AND ($2='' OR status=$2) AND ($3='' OR name ILIKE $4) AND ($5::bigint IS NULL OR owner_user_id=$5)`, kind, status, search, pattern, ownerID).Scan(&total)
	if err != nil {
		return nil, Page{}, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,kind,name,status,owner_user_id,data,created_by,updated_by,created_at,updated_at FROM resources WHERE kind=$1 AND ($2='' OR status=$2) AND ($3='' OR name ILIKE $4) AND ($5::bigint IS NULL OR owner_user_id=$5) ORDER BY `+orderBy(resourceSortColumns, sort, "updated_at DESC", "id")+` LIMIT $6 OFFSET $7`, kind, status, search, pattern, ownerID, pageSize, offset)
	if err != nil {
		return nil, Page{}, err
	}
	defer rows.Close()
	items := []Resource{}
	for rows.Next() {
		var r Resource
		var data []byte
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.Status, &r.OwnerUserID, &data, &r.CreatedBy, &r.UpdatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, Page{}, err
		}
		r.Data = json.RawMessage(data)
		items = append(items, r)
	}
	return items, Page{Page: page, PageSize: pageSize, Total: total}, rows.Err()
}

func (s *Store) UpdateResource(ctx context.Context, kind string, id int64, input ResourceWrite, actorID int64) (Resource, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if len(input.Data) == 0 {
		input.Data = json.RawMessage(`{}`)
	}
	result, err := s.Pool.Exec(ctx, `UPDATE resources SET name=$3,status=$4,owner_user_id=$5,data=$6,updated_by=$7,updated_at=now() WHERE kind=$1 AND id=$2`, kind, id, input.Name, input.Status, input.OwnerUserID, input.Data, actorID)
	if err != nil {
		return Resource{}, err
	}
	if result.RowsAffected() == 0 {
		return Resource{}, ErrNotFound
	}
	return s.GetResource(ctx, kind, id)
}

func (s *Store) DeleteResource(ctx context.Context, kind string, id int64) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM resources WHERE kind=$1 AND id=$2`, kind, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Dashboard(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"sampled_at": time.Now().UTC()}
	var features struct {
		GPUMonitoring bool `json:"gpu_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	queries := map[string]string{
		"hubs":           `SELECT count(*) FROM hubs WHERE enabled`,
		"degraded_hubs":  `SELECT count(*) FROM hubs WHERE enabled AND status NOT IN ('healthy','ok')`,
		"users":          `SELECT count(*) FROM managed_users u JOIN hubs h ON h.id=u.hub_id WHERE u.active AND h.enabled`,
		"active_users":   `SELECT count(DISTINCT (s.hub_id,s.username)) FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.status='running' AND h.enabled`,
		"servers":        `SELECT count(*) FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.status='running' AND h.enabled`,
		"incidents_open": `SELECT count(*) FROM resources WHERE kind='incident' AND status NOT IN ('resolved','closed')`,
		"approvals_open": `SELECT count(*) FROM resources WHERE kind='approval' AND status IN ('pending_review','pending','executing')`,
	}
	if features.GPUMonitoring {
		queries["gpu_users"] = `SELECT count(DISTINCT (s.hub_id,s.username)) FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.status='running' AND h.enabled AND COALESCE(s.gpu_count,0)>0`
	}
	for key, query := range queries {
		var count int64
		if err := s.Pool.QueryRow(ctx, query).Scan(&count); err != nil {
			return nil, err
		}
		result[key] = count
	}
	var freshness *time.Time
	_ = s.Pool.QueryRow(ctx, `SELECT max(ts) FROM (
		SELECT max(last_seen_at) ts FROM hubs WHERE enabled
		UNION ALL SELECT max(s.synced_at) FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE h.enabled
		UNION ALL SELECT max(sampled_at) FROM metric_samples WHERE ($1 OR metric_kind<>'gpu')
	) x`, features.GPUMonitoring).Scan(&freshness)
	var enabledHubs, staleHubs int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status NOT IN ('healthy','ok') OR last_seen_at IS NULL OR last_seen_at < now()-make_interval(secs=>GREATEST(300,collect_interval_seconds*2))) FROM hubs WHERE enabled`).Scan(&enabledHubs, &staleHubs); err != nil {
		return nil, err
	}
	result["data_freshness"] = freshness
	result["stale"] = staleHubs > 0 || enabledHubs == 0
	return result, nil
}

func (s *Store) LiveSnapshot(ctx context.Context) (map[string]any, error) {
	base, err := s.Dashboard(ctx)
	if err != nil {
		return nil, err
	}
	var features struct {
		GPUMonitoring      bool `json:"gpu_monitoring"`
		LLMUsageMonitoring bool `json:"llm_usage_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	var resourceSource struct {
		Enabled bool `json:"enabled"`
	}
	_ = s.GetSetting(ctx, "prometheus", &resourceSource)
	base["feature_enabled"] = map[string]bool{"gpu_monitoring": features.GPUMonitoring, "llm_usage_monitoring": features.LLMUsageMonitoring}
	if !features.GPUMonitoring {
		delete(base, "gpu_users")
	}
	rows, err := s.Pool.Query(ctx, `SELECT s.id,h.id,h.name,h.network,h.collect_interval_seconds,s.username,COALESCE(u.department,''),s.server_name,s.status,s.started_at,s.last_activity_at,s.cpu_cores,s.memory_bytes,s.gpu_count,s.raw,s.synced_at FROM servers s JOIN hubs h ON h.id=s.hub_id LEFT JOIN managed_users u ON u.id=s.managed_user_id WHERE s.status='running' AND h.enabled ORDER BY h.name,s.username,s.server_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []map[string]any{}
	freshSessions := []map[string]any{}
	freshUsers := map[string]bool{}
	aggregates := map[string]map[string]float64{}
	now := time.Now().UTC()
	resourceSourceStale := false
	for rows.Next() {
		var serverID, hubID int64
		var hub, network, username, department, serverName, status string
		var collectInterval int
		var started, activity *time.Time
		var cpu *float64
		var memory *int64
		var gpu *int
		var raw []byte
		var synced time.Time
		if err := rows.Scan(&serverID, &hubID, &hub, &network, &collectInterval, &username, &department, &serverName, &status, &started, &activity, &cpu, &memory, &gpu, &raw, &synced); err != nil {
			return nil, err
		}
		var detail map[string]any
		_ = json.Unmarshal(raw, &detail)
		project, _ := detail["project"].(string)
		sessionStale := sessionSnapshotStale(now, synced, collectInterval)
		cpuFresh := cpu != nil && metricSampleFresh(now, detail, "cpu_sampled_at", synced, resourceSource.Enabled)
		memoryFresh := memory != nil && metricSampleFresh(now, detail, "memory_sampled_at", synced, resourceSource.Enabled)
		gpuFresh := gpu != nil && metricSampleFresh(now, detail, "gpu_sampled_at", synced, resourceSource.Enabled)
		gpuExpected := gpuMetricsExpected(features.GPUMonitoring, gpu, detail)
		var cpuValue, memoryValue any
		if cpuFresh {
			cpuValue = *cpu
		}
		if memoryFresh {
			memoryValue = *memory
		}
		vram, gpuUtil, gpuValue := float64(0), float64(0), float64(0)
		if features.GPUMonitoring {
			if gpuFresh {
				vram = number(detail["vram_bytes"])
				gpuUtil = number(detail["gpu_utilization"])
				gpuValue = float64(derefInt(gpu))
			}
		}
		runtimeSeconds := float64(0)
		if started != nil {
			runtimeSeconds = now.Sub(*started).Seconds()
		}
		idle := activity != nil && cpuFresh && now.Sub(*activity) > 2*time.Hour && number(cpuValue) < .03 && (!features.GPUMonitoring || (gpuFresh && gpuUtil < 5))
		resourceStale := resourceSource.Enabled && (!cpuFresh || !memoryFresh || (gpuExpected && !gpuFresh))
		resourceSourceStale = resourceSourceStale || resourceStale
		session := map[string]any{"server_id": serverID, "hub_id": hubID, "hub": hub, "network": network, "department": department, "project": project, "username": username, "server_name": serverName, "status": status, "started_at": started, "last_activity_at": activity, "runtime_seconds": runtimeSeconds, "cpu_cores": cpuValue, "memory_bytes": memoryValue, "idle_candidate": idle, "sampled_at": synced, "stale": sessionStale, "data_freshness": synced, "resource_stale": resourceStale, "resource_sampled_at": latestMetricSample(detail)}
		if features.GPUMonitoring {
			if gpuFresh {
				session["gpu_count"], session["gpu_utilization"], session["vram_bytes"] = gpuValue, gpuUtil, vram
			} else {
				session["gpu_count"], session["gpu_utilization"], session["vram_bytes"] = nil, nil, nil
			}
			wasteCandidate, wasteScore := gpuWasteAssessment(gpuValue, gpuUtil, runtimeSeconds, gpuFresh)
			session["waste_candidate"], session["waste_score"] = wasteCandidate, wasteScore
		}
		sessions = append(sessions, session)
		if sessionStale {
			continue
		}
		freshSessions = append(freshSessions, session)
		freshUsers[fmt.Sprintf("%d\x00%s", hubID, username)] = true
		for _, key := range []string{"all", "hub:" + hub, "network:" + network, "department:" + department, "project:" + project} {
			if aggregates[key] == nil {
				aggregates[key] = map[string]float64{}
			}
			aggregates[key]["servers"]++
			if cpuFresh {
				aggregates[key]["cpu_cores"] += number(cpuValue)
				aggregates[key]["cpu_samples"]++
			}
			if memoryFresh {
				aggregates[key]["memory_bytes"] += number(memoryValue)
				aggregates[key]["memory_samples"]++
			}
			if features.GPUMonitoring {
				if gpuFresh {
					aggregates[key]["gpu_count"] += gpuValue
					aggregates[key]["vram_bytes"] += vram
					aggregates[key]["gpu_samples"]++
				}
			}
		}
	}
	summary := map[string]any{"active_users": len(freshUsers), "running_servers": len(freshSessions), "idle_sessions": countBool(freshSessions, "idle_candidate"), "long_running_sessions": countRuntime(freshSessions, 24*time.Hour)}
	if aggregates["all"]["cpu_samples"] > 0 {
		summary["cpu_usage"] = aggregates["all"]["cpu_cores"]
	}
	if aggregates["all"]["memory_samples"] > 0 {
		summary["memory_usage"] = aggregates["all"]["memory_bytes"]
	}
	if features.GPUMonitoring && aggregates["all"]["gpu_samples"] > 0 {
		summary["gpu_usage"], summary["vram_usage"] = aggregates["all"]["gpu_count"], aggregates["all"]["vram_bytes"]
	}
	base["summary"] = summary
	base["active_users"], base["servers"] = len(freshUsers), len(freshSessions)
	base["sessions"] = sessions
	base["live_users"] = sessions
	base["aggregates"] = aggregates
	base["usage_trend"] = []any{}
	base["top_users"] = topLiveUsers(freshSessions)
	if features.GPUMonitoring {
		base["gpu_waste"] = filterBool(freshSessions, "waste_candidate")
	} else {
		delete(base, "gpu_waste")
	}
	base["filters"] = map[string]any{"group_by": []string{"hub", "network", "department", "project"}}
	hubs, hubsErr := s.ListHubs(ctx)
	if hubsErr != nil {
		return nil, hubsErr
	}
	userTotals, totalsErr := s.hubUserTotals(ctx)
	if totalsErr != nil {
		return nil, totalsErr
	}
	base["hubs"] = buildLiveHubCards(hubs, freshSessions, userTotals, now)
	if features.LLMUsageMonitoring {
		llm, _ := s.LLMUsage(ctx, now.Add(-time.Hour), now, "user")
		base["llm_usage"] = llm
	} else {
		base["llm_usage"] = map[string]any{"feature_enabled": false, "data": []any{}}
	}
	base["sampled_at"] = now
	if resourceSourceStale {
		base["stale"] = true
	}
	return base, rows.Err()
}

func (s *Store) hubUserTotals(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.Pool.Query(ctx, `SELECT u.hub_id,count(*) FROM managed_users u JOIN hubs h ON h.id=u.hub_id WHERE u.active AND h.enabled GROUP BY u.hub_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := map[int64]int64{}
	for rows.Next() {
		var hubID, total int64
		if err := rows.Scan(&hubID, &total); err != nil {
			return nil, err
		}
		totals[hubID] = total
	}
	return totals, rows.Err()
}

func buildLiveHubCards(hubs []Hub, sessions []map[string]any, totalUsers map[int64]int64, now time.Time) []map[string]any {
	type hubStats struct {
		servers int
		users   map[string]bool
	}
	stats := map[int64]*hubStats{}
	enabled := map[int64]bool{}
	for _, hub := range hubs {
		enabled[hub.ID] = hub.Enabled
	}
	for _, session := range sessions {
		hubID, _ := session["hub_id"].(int64)
		if !enabled[hubID] {
			continue
		}
		if stats[hubID] == nil {
			stats[hubID] = &hubStats{users: map[string]bool{}}
		}
		stats[hubID].servers++
		if username, _ := session["username"].(string); username != "" {
			stats[hubID].users[username] = true
		}
	}
	result := make([]map[string]any, 0, len(hubs))
	for _, hub := range hubs {
		activeUsers, runningServers := 0, 0
		totalUsersForHub := totalUsers[hub.ID]
		if stat := stats[hub.ID]; stat != nil {
			activeUsers, runningServers = len(stat.users), stat.servers
		}
		status := hub.Status
		if !hub.Enabled {
			status = "disabled"
			totalUsersForHub = 0
		}
		result = append(result, map[string]any{
			"id": hub.ID, "name": hub.Name, "network": hub.Network, "status": status, "version": hub.Version,
			"enabled": hub.Enabled, "active_users": activeUsers, "running_servers": runningServers, "total_users": totalUsersForHub,
			"last_seen_at": hub.LastSeenAt, "last_success_at": hub.LastSeenAt, "last_error": hub.LastError,
			"stale": hub.Enabled && (hub.Status != "healthy" && hub.Status != "ok" || hub.LastSeenAt == nil || sessionSnapshotStale(now, valueOrZero(hub.LastSeenAt), hub.CollectIntervalSeconds)),
		})
	}
	return result
}

func gpuWasteAssessment(gpuCount, utilization, runtimeSeconds float64, fresh bool) (bool, float64) {
	if !fresh || gpuCount <= 0 || runtimeSeconds < 30*60 || utilization >= 5 {
		return false, 0
	}
	return true, max(0, min(100, 100-utilization))
}

func metricSampleFresh(now time.Time, detail map[string]any, key string, syncedAt time.Time, requireMetricTimestamp bool) bool {
	sampled := metricSampleTime(detail[key])
	if sampled == nil && requireMetricTimestamp {
		return false
	}
	if sampled == nil {
		sampled = metricSampleTime(detail["resource_sampled_at"])
	}
	if sampled == nil && !requireMetricTimestamp && !syncedAt.IsZero() {
		sampled = &syncedAt
	}
	return sampled != nil && !sampled.After(now.Add(time.Minute)) && now.Sub(*sampled) <= 5*time.Minute
}

func metricSampleTime(value any) *time.Time {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil
	}
	return &parsed
}

func latestMetricSample(detail map[string]any) any {
	for _, key := range []string{"resource_sampled_at", "cpu_sampled_at", "memory_sampled_at", "gpu_sampled_at"} {
		if value, ok := detail[key]; ok {
			return value
		}
	}
	return nil
}

func gpuMetricsExpected(enabled bool, gpuCount *int, detail map[string]any) bool {
	if !enabled {
		return false
	}
	return (gpuCount != nil && *gpuCount > 0) || detail["gpu_utilization"] != nil || detail["vram_bytes"] != nil || detail["gpu_sampled_at"] != nil
}

func sessionSnapshotStale(now, syncedAt time.Time, collectIntervalSeconds int) bool {
	window := 5 * time.Minute
	if configured := 2 * time.Duration(collectIntervalSeconds) * time.Second; configured > window {
		window = configured
	}
	return syncedAt.IsZero() || now.Sub(syncedAt) > window
}

func countBool(items []map[string]any, key string) int {
	count := 0
	for _, item := range items {
		if v, _ := item[key].(bool); v {
			count++
		}
	}
	return count
}

func countRuntime(items []map[string]any, threshold time.Duration) int {
	count := 0
	for _, item := range items {
		if v, _ := item["runtime_seconds"].(float64); v >= threshold.Seconds() {
			count++
		}
	}
	return count
}

func filterBool(items []map[string]any, key string) []map[string]any {
	result := []map[string]any{}
	for _, item := range items {
		if v, _ := item[key].(bool); v {
			result = append(result, item)
		}
	}
	return result
}

func topLiveUsers(items []map[string]any) []map[string]any {
	byUser := map[string]map[string]any{}
	for _, item := range items {
		username, _ := item["username"].(string)
		if byUser[username] == nil {
			byUser[username] = map[string]any{"username": username, "servers": 0, "runtime_seconds": float64(0), "cpu_cores": float64(0), "memory_bytes": float64(0)}
		}
		byUser[username]["servers"] = byUser[username]["servers"].(int) + 1
		byUser[username]["runtime_seconds"] = byUser[username]["runtime_seconds"].(float64) + number(item["runtime_seconds"])
		byUser[username]["cpu_cores"] = byUser[username]["cpu_cores"].(float64) + number(item["cpu_cores"])
		byUser[username]["memory_bytes"] = byUser[username]["memory_bytes"].(float64) + number(item["memory_bytes"])
	}
	result := make([]map[string]any, 0, len(byUser))
	for _, item := range byUser {
		item["usage_hours"] = item["runtime_seconds"].(float64) / 3600
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i]["runtime_seconds"].(float64), result[j]["runtime_seconds"].(float64)
		if left == right {
			return result[i]["username"].(string) < result[j]["username"].(string)
		}
		return left > right
	})
	return result
}

func number(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// Usage reports facts recorded during the requested interval. Hub.enabled only
// controls current collection and live views; disabling a Hub must not rewrite
// or hide its already-recorded audit/usage history.
func (s *Store) Usage(ctx context.Context, from, to time.Time, granularity, groupBy string) (map[string]any, error) {
	var features struct {
		GPUMonitoring bool `json:"gpu_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	allowedGranularity := map[string]string{"hour": "hour", "day": "day", "week": "week", "month": "month"}
	unit, ok := allowedGranularity[granularity]
	if !ok {
		unit = "day"
	}
	allowedGroup := map[string]string{"user": "labels->>'username'", "hub": "labels->>'hub'", "network": "labels->>'network'", "department": "labels->>'department'", "project": "labels->>'project'"}
	groupExpr, ok := allowedGroup[groupBy]
	if !ok {
		groupBy, groupExpr = "user", "labels->>'username'"
	}
	// Server runtime is calculated from server intervals below. Excluding runtime
	// counters here prevents the same usage from appearing twice in the trend.
	query := `SELECT date_trunc('` + unit + `',sampled_at) bucket,COALESCE(` + groupExpr + `,'unknown') grouping,metric_name,avg(value),max(value) FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND metric_name NOT IN ('runtime_seconds','server_running_seconds') AND ($3 OR metric_kind<>'gpu') GROUP BY 1,2,3 ORDER BY 1,2`
	rows, err := s.Pool.Query(ctx, query, from, to, features.GPUMonitoring)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trend := []map[string]any{}
	for rows.Next() {
		var bucket time.Time
		var group, metric string
		var avg, max float64
		if err := rows.Scan(&bucket, &group, &metric, &avg, &max); err != nil {
			return nil, err
		}
		trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": metric, "average": avg, "peak": max})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	serverGroupExpr := map[string]string{"user": "ss.username", "hub": "h.name", "network": "h.network", "department": "COALESCE(u.department,'unknown')", "project": "COALESCE(s.raw->>'project','unknown')"}[groupBy]
	intervalStep := map[string]string{"hour": "1 hour", "day": "1 day", "week": "1 week", "month": "1 month"}[unit]
	serverTrendSQL := `WITH buckets AS (
		SELECT bucket, bucket+interval '` + intervalStep + `' bucket_end
		FROM generate_series(date_trunc('` + unit + `',$1::timestamptz),date_trunc('` + unit + `',$2::timestamptz),interval '` + intervalStep + `') bucket
	), server_intervals AS (
		SELECT ss.id,COALESCE(` + serverGroupExpr + `,'unknown') grouping,ss.started_at original_start,
		       GREATEST(ss.started_at,$1::timestamptz) active_from,
		       LEAST(COALESCE(ss.ended_at,$2::timestamptz),$2::timestamptz) active_to
		FROM server_sessions ss JOIN hubs h ON h.id=ss.hub_id LEFT JOIN servers s ON s.id=ss.server_id LEFT JOIN managed_users u ON u.id=s.managed_user_id
		WHERE ss.started_at<$2::timestamptz
		  AND COALESCE(ss.ended_at,$2::timestamptz)>$1::timestamptz
	)
	SELECT b.bucket,si.grouping,
	       count(*) FILTER(WHERE si.original_start>=GREATEST(b.bucket,$1::timestamptz) AND si.original_start<LEAST(b.bucket_end,$2::timestamptz)),
	       COALESCE(sum(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(si.active_to,b.bucket_end,$2::timestamptz)-GREATEST(si.active_from,b.bucket,$1::timestamptz))))),0)::double precision
	FROM buckets b JOIN server_intervals si ON si.active_from<LEAST(b.bucket_end,$2::timestamptz) AND si.active_to>GREATEST(b.bucket,$1::timestamptz)
	GROUP BY b.bucket,si.grouping ORDER BY b.bucket,si.grouping`
	serverRows, err := s.Pool.Query(ctx, serverTrendSQL, from, to)
	if err != nil {
		return nil, err
	}
	for serverRows.Next() {
		var bucket time.Time
		var group string
		var starts int64
		var runtime float64
		if err := serverRows.Scan(&bucket, &group, &starts, &runtime); err != nil {
			serverRows.Close()
			return nil, err
		}
		trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": "server_starts", "average": float64(starts), "peak": float64(starts)}, map[string]any{"bucket": bucket, "group": group, "metric": "runtime_seconds", "average": runtime, "peak": runtime})
	}
	if err := serverRows.Err(); err != nil {
		serverRows.Close()
		return nil, err
	}
	serverRows.Close()
	if groupBy == "user" {
		loginRows, err := s.Pool.Query(ctx, `SELECT date_trunc('`+unit+`',created_at),COALESCE(NULLIF(actor_username,''),'unknown'),count(*) FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND created_at BETWEEN $1 AND $2 GROUP BY 1,2 ORDER BY 1,2`, from, to)
		if err != nil {
			return nil, err
		}
		for loginRows.Next() {
			var bucket time.Time
			var group string
			var count int64
			if err := loginRows.Scan(&bucket, &group, &count); err != nil {
				loginRows.Close()
				return nil, err
			}
			trend = append(trend, map[string]any{"bucket": bucket, "group": group, "metric": "login_count", "average": float64(count), "peak": float64(count)})
		}
		if err := loginRows.Err(); err != nil {
			loginRows.Close()
			return nil, err
		}
		loginRows.Close()
	}
	result := map[string]any{"from": from, "to": to, "granularity": unit, "group_by": groupBy, "trend": trend, "top_users": []any{}, "dau": 0, "wau": 0, "mau": 0}
	for label, since := range map[string]time.Time{"dau": to.Add(-24 * time.Hour), "wau": to.Add(-7 * 24 * time.Hour), "mau": to.Add(-30 * 24 * time.Hour)} {
		var count int
		if err := s.Pool.QueryRow(ctx, usageActiveCountSQL, since, to, features.GPUMonitoring).Scan(&count); err != nil {
			return nil, err
		}
		result[label] = count
	}
	topRows, err := s.Pool.Query(ctx, usageTopUsersSQL, from, to, features.GPUMonitoring)
	if err != nil {
		return nil, err
	}
	top := []map[string]any{}
	for topRows.Next() {
		var username string
		var servers, logins int64
		var runtime, cpuAvg, cpuPeak, memoryAvg, memoryPeak, gpuAvg, gpuPeak float64
		if err := topRows.Scan(&username, &servers, &logins, &runtime, &cpuAvg, &cpuPeak, &memoryAvg, &memoryPeak, &gpuAvg, &gpuPeak); err != nil {
			topRows.Close()
			return nil, err
		}
		item := map[string]any{"username": username, "servers": servers, "login_count": logins, "runtime_seconds": runtime, "cpu_average": cpuAvg, "cpu_peak": cpuPeak, "memory_average": memoryAvg, "memory_peak": memoryPeak}
		if features.GPUMonitoring {
			item["gpu_average"], item["gpu_peak"] = gpuAvg, gpuPeak
		}
		top = append(top, item)
	}
	if err := topRows.Err(); err != nil {
		topRows.Close()
		return nil, err
	}
	topRows.Close()
	result["top_users"] = top
	var freshness *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT max(ts) FROM (SELECT max(sampled_at) ts FROM metric_samples WHERE ($1 OR metric_kind<>'gpu') UNION ALL SELECT max(created_at) FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' UNION ALL SELECT max(synced_at) FROM servers) freshness`, features.GPUMonitoring).Scan(&freshness); err != nil {
		return nil, err
	}
	result["data_freshness"] = freshness
	result["stale"] = freshness == nil || time.Since(*freshness) > 5*time.Minute
	return result, nil
}

const usageActiveCountSQL = `WITH activity AS (
 SELECT labels->>'username' username,sampled_at occurred_at FROM metric_samples WHERE NULLIF(labels->>'username','') IS NOT NULL AND ($3 OR metric_kind<>'gpu')
 UNION ALL SELECT actor_username,created_at FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND actor_username<>''
 UNION ALL SELECT username,last_activity_at FROM managed_users WHERE last_activity_at IS NOT NULL
 UNION ALL SELECT username,COALESCE(last_activity_at,started_at,synced_at) FROM servers
) SELECT count(DISTINCT username) FROM activity WHERE occurred_at BETWEEN $1 AND $2`

const usageTopUsersSQL = `WITH samples AS (
 SELECT labels->>'username' username,
	count(*) FILTER(WHERE metric_name IN ('runtime_seconds','server_running_seconds')) runtime_sample_count,
  COALESCE(sum(value) FILTER(WHERE metric_name IN ('runtime_seconds','server_running_seconds')),0) runtime_seconds,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('cpu','cpu_cores','cpu_usage')),0) cpu_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('cpu','cpu_cores','cpu_usage')),0) cpu_peak,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('memory','memory_bytes','memory_usage')),0) memory_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('memory','memory_bytes','memory_usage')),0) memory_peak,
  COALESCE(avg(value) FILTER(WHERE metric_name IN ('gpu','gpu_utilization')),0) gpu_average,
  COALESCE(max(value) FILTER(WHERE metric_name IN ('gpu','gpu_utilization')),0) gpu_peak
 FROM metric_samples WHERE sampled_at BETWEEN $1 AND $2 AND NULLIF(labels->>'username','') IS NOT NULL AND ($3 OR metric_kind<>'gpu') GROUP BY 1
), server_usage AS (
 SELECT username,count(*) servers,
  COALESCE(sum(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$2::timestamptz),$2::timestamptz)-GREATEST(started_at,$1::timestamptz))))),0) runtime_seconds
 FROM server_sessions WHERE started_at<=$2::timestamptz AND COALESCE(ended_at,$2::timestamptz)>=$1::timestamptz GROUP BY username
), logins AS (SELECT actor_username username,count(*) login_count FROM audit_logs WHERE action IN ('auth.login','auth.oidc') AND result='success' AND created_at BETWEEN $1 AND $2 AND actor_username<>'' GROUP BY actor_username),
 names AS (SELECT username FROM samples UNION SELECT username FROM server_usage UNION SELECT username FROM logins)
 SELECT names.username,COALESCE(server_usage.servers,0),COALESCE(logins.login_count,0),CASE WHEN COALESCE(samples.runtime_sample_count,0)>0 THEN samples.runtime_seconds ELSE COALESCE(server_usage.runtime_seconds,0) END,COALESCE(samples.cpu_average,0),COALESCE(samples.cpu_peak,0),COALESCE(samples.memory_average,0),COALESCE(samples.memory_peak,0),COALESCE(samples.gpu_average,0),COALESCE(samples.gpu_peak,0)
 FROM names LEFT JOIN samples USING(username) LEFT JOIN server_usage USING(username) LEFT JOIN logins USING(username) ORDER BY 4 DESC,3 DESC LIMIT 20`

func (s *Store) DeleteSecret(ctx context.Context, key string) error {
	result, err := s.Pool.Exec(ctx, `DELETE FROM secrets WHERE secret_key=$1`, key)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) LLMUsage(ctx context.Context, from, to time.Time, groupBy string) (map[string]any, error) {
	groupColumns := map[string]string{"user": "COALESCE(username,'unknown')", "pod": "pod_name", "model": "model"}
	if groupBy != "detail" {
		if _, ok := groupColumns[groupBy]; !ok {
			groupBy = "user"
		}
	}
	var features struct {
		LLMUsageMonitoring bool `json:"llm_usage_monitoring"`
	}
	_ = s.GetSetting(ctx, "features", &features)
	if !features.LLMUsageMonitoring {
		empty := []map[string]any{}
		return map[string]any{
			"feature_enabled": false, "from": from, "to": to, "group_by": groupBy,
			"data": empty, "top_callers": empty, "breakdown": empty, "usage_trend": empty,
			"summary": summarizeLLMItems(empty), "data_freshness": nil, "stale": false,
		}, nil
	}
	if groupBy == "detail" {
		rows, err := s.Pool.Query(ctx, `SELECT COALESCE(username,'unknown'),pod_name,hub_name,model,sum(calls),sum(success_count),sum(error_count),max(latency_p50_ms),max(latency_p95_ms),sum(input_tokens),sum(output_tokens),sum(total_tokens),sum(bytes),sum(estimated_cost),max(sampled_at) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1,2,3,4 ORDER BY sum(calls) DESC`, from, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []map[string]any{}
		var freshness *time.Time
		for rows.Next() {
			var username, pod, hub, model string
			var calls, success, fail int64
			var p50, p95 *float64
			var input, output, total, byteCount *int64
			var cost *float64
			var sampled time.Time
			if err := rows.Scan(&username, &pod, &hub, &model, &calls, &success, &fail, &p50, &p95, &input, &output, &total, &byteCount, &cost, &sampled); err != nil {
				return nil, err
			}
			items = append(items, map[string]any{"username": username, "pod_name": pod, "hub_name": hub, "model": model, "calls": calls, "success": success, "errors": fail, "success_rate": observedSuccessRate(success, fail), "status_observed_calls": success + fail, "latency_p50_ms": p50, "latency_p95_ms": p95, "input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": byteCount, "estimated_cost": cost, "sampled_at": sampled})
			if freshness == nil || sampled.After(*freshness) {
				t := sampled
				freshness = &t
			}
		}
		trendRows, trendErr := s.Pool.Query(ctx, `SELECT date_trunc('hour',sampled_at),sum(calls),sum(total_tokens),sum(estimated_cost) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1 ORDER BY 1`, from, to)
		trend := []map[string]any{}
		if trendErr == nil {
			for trendRows.Next() {
				var bucket time.Time
				var calls int64
				var tokens *int64
				var cost *float64
				if trendRows.Scan(&bucket, &calls, &tokens, &cost) == nil {
					trend = append(trend, map[string]any{"bucket": bucket, "calls": calls, "total_tokens": tokens, "estimated_cost": cost})
				}
			}
			trendRows.Close()
		}
		return map[string]any{"feature_enabled": true, "from": from, "to": to, "group_by": "detail", "data": items, "top_callers": items, "summary": summarizeLLMItems(items), "breakdown": buildLLMBreakdown(items), "usage_trend": trend, "data_freshness": freshness, "stale": s.llmUsageStale(ctx, freshness)}, rows.Err()
	}
	expr := groupColumns[groupBy]
	rows, err := s.Pool.Query(ctx, `SELECT `+expr+` grouping,sum(calls),sum(success_count),sum(error_count),sum(input_tokens),sum(output_tokens),sum(total_tokens),sum(bytes),sum(estimated_cost),max(sampled_at) FROM llm_usage_samples WHERE sampled_at BETWEEN $1 AND $2 GROUP BY 1 ORDER BY sum(calls) DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	var freshness *time.Time
	for rows.Next() {
		var group string
		var calls, success, fail int64
		var input, output, total, bytes *int64
		var cost any
		var sampled time.Time
		if err := rows.Scan(&group, &calls, &success, &fail, &input, &output, &total, &bytes, &cost, &sampled); err != nil {
			return nil, err
		}
		if freshness == nil || sampled.After(*freshness) {
			t := sampled
			freshness = &t
		}
		items = append(items, map[string]any{"group": group, "calls": calls, "success": success, "errors": fail, "input_tokens": input, "output_tokens": output, "total_tokens": total, "bytes": bytes, "estimated_cost": cost, "sampled_at": sampled})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detailView, err := s.LLMUsage(ctx, from, to, "detail")
	if err != nil {
		return nil, err
	}
	return map[string]any{"feature_enabled": true, "from": from, "to": to, "group_by": groupBy, "data": items, "top_callers": items, "summary": detailView["summary"], "breakdown": detailView["breakdown"], "usage_trend": detailView["usage_trend"], "data_freshness": detailView["data_freshness"], "stale": detailView["stale"]}, nil
}

func (s *Store) llmUsageStale(ctx context.Context, freshness *time.Time) bool {
	if freshness == nil {
		return true
	}
	var cfg struct {
		StaleSeconds int `json:"stale_seconds"`
	}
	_ = s.GetSetting(ctx, "llm_usage", &cfg)
	if cfg.StaleSeconds < 30 || cfg.StaleSeconds > 86400 {
		cfg.StaleSeconds = 300
	}
	return time.Since(*freshness) > time.Duration(cfg.StaleSeconds)*time.Second
}

func summarizeLLMItems(items []map[string]any) map[string]any {
	var calls, success, failures int64
	var input, output, total, byteCount int64
	var cost, p50, p95 float64
	hasInput, hasOutput, hasTotal, hasBytes := false, false, false, false
	hasCost, hasP50, hasP95 := false, false, false
	for _, item := range items {
		calls += int64(anyNumber(item["calls"]))
		success += int64(anyNumber(item["success"]))
		failures += int64(anyNumber(item["errors"]))
		if value, ok := optionalNumber(item["input_tokens"]); ok {
			input += int64(value)
			hasInput = true
		}
		if value, ok := optionalNumber(item["output_tokens"]); ok {
			output += int64(value)
			hasOutput = true
		}
		if value, ok := optionalNumber(item["total_tokens"]); ok {
			total += int64(value)
			hasTotal = true
		}
		if value, ok := optionalNumber(item["bytes"]); ok {
			byteCount += int64(value)
			hasBytes = true
		}
		if value, ok := optionalNumber(item["estimated_cost"]); ok {
			cost += value
			hasCost = true
		}
		if value, ok := optionalNumber(item["latency_p50_ms"]); ok {
			if !hasP50 || value > p50 {
				p50 = value
			}
			hasP50 = true
		}
		if value, ok := optionalNumber(item["latency_p95_ms"]); ok {
			if !hasP95 || value > p95 {
				p95 = value
			}
			hasP95 = true
		}
	}
	summary := map[string]any{"calls": calls, "success": success, "errors": failures, "success_rate": observedSuccessRate(success, failures), "status_observed_calls": success + failures, "input_tokens": nil, "output_tokens": nil, "total_tokens": nil, "bytes": nil, "estimated_cost": nil, "latency_p50_ms": nil, "latency_p95_ms": nil}
	if hasInput {
		summary["input_tokens"] = input
	}
	if hasOutput {
		summary["output_tokens"] = output
	}
	if hasTotal {
		summary["total_tokens"] = total
	}
	if hasBytes {
		summary["bytes"] = byteCount
	}
	if hasCost {
		summary["estimated_cost"] = cost
	}
	if hasP50 {
		summary["latency_p50_ms"] = p50
	}
	if hasP95 {
		summary["latency_p95_ms"] = p95
	}
	return summary
}

func observedSuccessRate(success, failures int64) any {
	observed := success + failures
	if observed <= 0 {
		return nil
	}
	return float64(success) / float64(observed)
}

func buildLLMBreakdown(items []map[string]any) []map[string]any {
	users := map[string]map[string][]map[string]any{}
	for _, item := range items {
		username, _ := item["username"].(string)
		pod, _ := item["pod_name"].(string)
		if users[username] == nil {
			users[username] = map[string][]map[string]any{}
		}
		users[username][pod] = append(users[username][pod], item)
	}
	userNames := make([]string, 0, len(users))
	for name := range users {
		userNames = append(userNames, name)
	}
	sort.Strings(userNames)
	result := make([]map[string]any, 0, len(userNames))
	for _, username := range userNames {
		podsMap := users[username]
		podNames := make([]string, 0, len(podsMap))
		all := []map[string]any{}
		for pod, models := range podsMap {
			podNames = append(podNames, pod)
			all = append(all, models...)
		}
		sort.Strings(podNames)
		pods := make([]map[string]any, 0, len(podNames))
		for _, pod := range podNames {
			models := podsMap[pod]
			sort.SliceStable(models, func(i, j int) bool {
				left, _ := models[i]["model"].(string)
				right, _ := models[j]["model"].(string)
				return left < right
			})
			pods = append(pods, map[string]any{"pod_name": pod, "summary": summarizeLLMItems(models), "models": models})
		}
		result = append(result, map[string]any{"username": username, "summary": summarizeLLMItems(all), "pods": pods})
	}
	return result
}

func anyNumber(value any) float64 { number, _ := optionalNumber(value); return number }
func optionalNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case *float64:
		if v != nil {
			return *v, true
		}
	case *int64:
		if v != nil {
			return float64(*v), true
		}
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	}
	return 0, false
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) }
