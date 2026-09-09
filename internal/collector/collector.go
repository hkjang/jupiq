package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

const (
	pruneInterval      = 24 * time.Hour
	pruneRetryInterval = 30 * time.Minute
	// hubCollectConcurrency caps how many hub probes run at the same time.
	// Each probe holds an outbound HTTP connection and database round trips
	// for up to 25 seconds, so an install with dozens of hubs would otherwise
	// open dozens of them at once on every 30-second cycle.
	hubCollectConcurrency = 8
	// hubShutdownGrace bounds how long Run waits for in-flight hub probes to
	// land their writes after the context is cancelled, so one wedged hub
	// cannot hold shutdown open indefinitely.
	hubShutdownGrace = 10 * time.Second
)

type Collector struct {
	Store       *store.Store
	Logger      *slog.Logger
	hubSlots    chan struct{}
	hubs        sync.WaitGroup
	mu          sync.Mutex
	last        map[int64]time.Time
	lastLLMScan time.Time
	lastPrune   time.Time
	pruneRetry  bool
}

func New(s *store.Store, logger *slog.Logger) *Collector {
	return &Collector{Store: s, Logger: logger, hubSlots: make(chan struct{}, hubCollectConcurrency), last: map[int64]time.Time{}}
}

func (c *Collector) Run(ctx context.Context) {
	// Hub probes outlive the cycle that started them, so wait for them here
	// rather than inside collect: blocking each cycle would delay the
	// Kubernetes and Prometheus collectors by up to a full probe timeout.
	defer c.waitForHubs()
	c.collect(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

// goHub runs one hub probe on its own goroutine, admitting at most
// hubCollectConcurrency of them at a time and registering it so waitForHubs
// can block on it. A probe still queued when the context is cancelled is
// dropped instead of starting work that can no longer be stored.
func (c *Collector) goHub(ctx context.Context, probe func()) {
	c.hubs.Add(1)
	go func() {
		defer c.hubs.Done()
		select {
		case c.hubSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-c.hubSlots }()
		probe()
	}()
}

// waitForHubs blocks until the running hub probes finish. Their health and
// snapshot writes deliberately run on a context detached from the collector's,
// so without this wait a shutdown would close the database pool underneath
// them and lose the degraded status that explains why collection stopped.
func (c *Collector) waitForHubs() {
	done := make(chan struct{})
	go func() {
		c.hubs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(hubShutdownGrace):
		c.Logger.Warn("hub collector shutdown wait timed out", "grace", hubShutdownGrace)
	}
}

func (c *Collector) collect(ctx context.Context) {
	c.collectHubs(ctx)
	c.collectKubernetes(ctx)
	c.collectPrometheus(ctx)
	c.collectLLMUsage(ctx)
	c.prune(ctx)
}

func (c *Collector) collectHubs(ctx context.Context) {
	hubs, err := c.Store.ListHubs(ctx)
	if err != nil {
		c.Logger.Warn("hub collector list failed", "error", err)
		return
	}
	for _, hub := range hubs {
		if !hub.Enabled || !c.due(hub) {
			continue
		}
		hub := hub
		c.goHub(ctx, func() {
			requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			currentHub, token, err := c.Store.GetHubCredential(requestCtx, hub.ID)
			if err != nil {
				c.Logger.Warn("hub credential read failed", "hub_id", hub.ID, "error", err)
				return
			}
			if !currentHub.Enabled {
				return
			}
			client, err := integration.NewJupyterHub(currentHub.BaseURL, token, currentHub.VerifyTLS)
			if err != nil {
				c.updateHubHealth(ctx, currentHub, false, "", err.Error(), nil)
				return
			}
			info, err := client.Info(requestCtx)
			if err != nil {
				c.updateHubHealth(ctx, currentHub, false, "", err.Error(), nil)
				return
			}
			users, err := client.Users(requestCtx)
			if err != nil {
				c.updateHubHealth(ctx, currentHub, false, info.Version, err.Error(), nil)
				return
			}
			stored, err := c.Store.SyncHubUsersIfCurrent(requestCtx, currentHub, users)
			if err != nil {
				c.updateHubHealth(ctx, currentHub, false, info.Version, err.Error(), nil)
				return
			}
			if !stored {
				c.Logger.Debug("discarded stale hub snapshot", "hub_id", hub.ID)
				return
			}
			c.updateHubHealth(ctx, currentHub, true, info.Version, "", map[string]any{"users": len(users), "synced_at": time.Now().UTC()})
		})
	}
}

func (c *Collector) updateHubHealth(parent context.Context, hub store.Hub, success bool, version, message string, snapshot any) {
	// Provider timeouts cancel requestCtx. Persist health through a separate,
	// short-lived context so the exact timeout that caused degradation cannot
	// also suppress the degraded status update.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	stored, err := c.Store.UpdateHubHealthIfCurrent(writeCtx, hub, success, version, message, snapshot)
	if err != nil {
		c.Logger.Warn("hub health update failed", "hub_id", hub.ID, "error", err)
	} else if !stored {
		c.Logger.Debug("discarded stale hub health", "hub_id", hub.ID)
	}
}

func (c *Collector) due(hub store.Hub) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	last := c.last[hub.ID]
	if !last.IsZero() && now.Sub(last) < time.Duration(hub.CollectIntervalSeconds)*time.Second {
		return false
	}
	c.last[hub.ID] = now
	return true
}

type prometheusConfig struct {
	Enabled   bool              `json:"enabled"`
	BaseURL   string            `json:"base_url"`
	VerifyTLS bool              `json:"verify_tls"`
	Queries   map[string]string `json:"queries"`
}

// prometheusQueries resolves the metric set one collection cycle runs, given a
// single gpu_monitoring snapshot. Re-reading the feature per metric would let a
// mid-cycle toggle collect some GPU metrics and skip others in the same pass;
// the authoritative gate is the feature check the store makes inside each write
// transaction, so one snapshot per cycle is enough here.
func prometheusQueries(configured map[string]string, gpuMonitoring bool) map[string]string {
	queries := make(map[string]string, len(configured)+3)
	for name, query := range configured {
		queries[name] = query
	}
	if len(queries) == 0 {
		queries["cpu_cores"] = `sum(rate(container_cpu_usage_seconds_total{pod=~"jupyter-.*"}[5m])) by (pod)`
		queries["memory_bytes"] = `sum(container_memory_working_set_bytes{pod=~"jupyter-.*"}) by (pod)`
	}
	if !gpuMonitoring {
		for name, query := range queries {
			if store.IsGPUMetric(name, query) {
				delete(queries, name)
			}
		}
		return queries
	}
	for name, query := range map[string]string{
		"gpu_count":       `count(DCGM_FI_DEV_GPU_UTIL{pod=~"jupyter-.*"}) by (pod)`,
		"gpu_utilization": `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`,
		"vram_bytes":      `sum(DCGM_FI_DEV_FB_USED * 1024 * 1024) by (pod)`,
	} {
		if _, ok := queries[name]; !ok {
			queries[name] = query
		}
	}
	return queries
}

func (c *Collector) collectPrometheus(ctx context.Context) {
	var cfg prometheusConfig
	token, _, generation, err := c.Store.GetSettingAndSecretGeneration(ctx, "prometheus", "prometheus.token", &cfg)
	if err != nil || !cfg.Enabled || cfg.BaseURL == "" {
		return
	}
	queries := prometheusQueries(cfg.Queries, c.featureEnabled(ctx, "gpu_monitoring"))
	client, err := integration.NewPrometheus(cfg.BaseURL, token, cfg.VerifyTLS)
	if err != nil {
		c.Logger.Warn("prometheus config invalid", "error", err)
		return
	}
	for name, query := range queries {
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		points, err := client.Query(requestCtx, query, time.Now())
		cancel()
		if err != nil {
			c.Logger.Warn("prometheus query failed", "metric", name, "error", err)
			continue
		}
		stored, err := c.Store.SavePrometheusMetricPointsIfCurrent(ctx, generation, name, query, points)
		if err != nil {
			c.Logger.Warn("save metric failed", "metric", name, "error", err)
		} else if !stored {
			c.Logger.Debug("discarded stale prometheus response", "metric", name)
			break
		}
	}
}

type kubernetesConfig struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"base_url"`
	VerifyTLS        bool   `json:"verify_tls"`
	Namespace        string `json:"namespace"`
	LabelSelector    string `json:"label_selector"`
	PodUsernameRegex string `json:"pod_username_regex"`
}

func (c *Collector) collectKubernetes(ctx context.Context) {
	settings, token, _, generation, err := c.Store.GetSettingsAndSecretGeneration(ctx, []string{"kubernetes", "llm_usage"}, "kubernetes.token")
	var cfg kubernetesConfig
	raw, exists := settings["kubernetes"]
	if err == nil && (!exists || json.Unmarshal(raw, &cfg) != nil) {
		err = store.ErrNotFound
	}
	if err != nil || !cfg.Enabled || cfg.BaseURL == "" {
		return
	}
	client, err := integration.NewKubernetes(cfg.BaseURL, token, cfg.VerifyTLS)
	if err != nil {
		c.Logger.Warn("kubernetes config invalid", "error", err)
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pods, err := client.Pods(requestCtx, cfg.Namespace, cfg.LabelSelector)
	if err != nil {
		c.Logger.Warn("kubernetes pods failed", "error", err)
		return
	}
	if cfg.PodUsernameRegex == "" {
		var llm struct {
			PodUsernameRegex string `json:"pod_username_regex"`
		}
		_ = json.Unmarshal(settings["llm_usage"], &llm)
		cfg.PodUsernameRegex = llm.PodUsernameRegex
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 20*time.Second)
	defer writeCancel()
	stored, err := c.Store.UpdatePodsIfCurrent(writeCtx, generation, pods, cfg.PodUsernameRegex)
	if err != nil {
		c.Logger.Warn("kubernetes pod save failed", "error", err)
	} else if !stored {
		c.Logger.Debug("discarded stale kubernetes response")
	}
}

type llmUsageConfig struct {
	Source               string            `json:"source"`
	PodUsernameRegex     string            `json:"pod_username_regex"`
	PathMatcher          string            `json:"path_matcher"`
	PromQL               map[string]string `json:"promql"`
	LabelMappings        map[string]string `json:"label_mappings"`
	InputCostPerMillion  float64           `json:"input_cost_per_million"`
	OutputCostPerMillion float64           `json:"output_cost_per_million"`
}

func (c *Collector) collectLLMUsage(ctx context.Context) {
	if !c.featureEnabled(ctx, "llm_usage_monitoring") {
		return
	}
	// The default query uses increase(...[1m]). Querying it on the 30-second
	// base ticker would count overlapping windows twice, so LLM counters have
	// their own non-overlapping one-minute cadence.
	scanAt := llmEvaluationTime(time.Now())
	if !c.llmScanDue(scanAt) {
		return
	}
	settings, token, _, generation, err := c.Store.GetSettingsAndSecretGeneration(ctx, []string{"llm_usage", "prometheus"}, "prometheus.token")
	if err != nil {
		return
	}
	var cfg llmUsageConfig
	if raw, ok := settings["llm_usage"]; !ok || json.Unmarshal(raw, &cfg) != nil || cfg.Source != "prometheus" {
		return
	}
	if _, err := integration.CompilePodUsernamePattern(cfg.PodUsernameRegex); err != nil {
		c.Logger.Warn("llm usage regex invalid", "error", err)
		return
	}
	if err := integration.ValidateLLMLabelMappings(cfg.LabelMappings); err != nil {
		c.Logger.Warn("llm usage label mapping invalid", "error", err)
		return
	}
	var prom prometheusConfig
	if raw, ok := settings["prometheus"]; !ok || json.Unmarshal(raw, &prom) != nil || !prom.Enabled || prom.BaseURL == "" {
		return
	}
	client, err := integration.NewPrometheus(prom.BaseURL, token, prom.VerifyTLS)
	if err != nil {
		return
	}
	queries := cfg.PromQL
	if len(queries) == 0 {
		queries = map[string]string{"calls": `sum(increase(http_requests_total{path="/v1/chat/completions"}[1m])) by (pod,path,status,model,hub)`}
	}
	for name, query := range queries {
		if !c.featureEnabled(ctx, "llm_usage_monitoring") {
			break
		}
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		points, queryErr := client.Query(requestCtx, query, scanAt)
		cancel()
		if queryErr != nil {
			c.Logger.Warn("llm usage query failed", "metric", name, "error", queryErr)
			continue
		}
		points = normalizeLLMLabels(points, cfg.LabelMappings)
		stored, err := c.Store.SaveLLMMetricPointsIfCurrent(ctx, generation, name, cfg.PodUsernameRegex, cfg.PathMatcher, points)
		if err != nil {
			c.Logger.Warn("llm usage save failed", "metric", name, "error", err)
		} else if !stored {
			c.Logger.Debug("discarded stale llm usage response", "metric", name)
			break
		}
		costRate := float64(0)
		if name == "input_tokens" {
			costRate = cfg.InputCostPerMillion
		} else if name == "output_tokens" {
			costRate = cfg.OutputCostPerMillion
		}
		if costRate > 0 {
			costPoints := make([]integration.MetricPoint, len(points))
			for index, point := range points {
				costPoints[index] = point
				costPoints[index].Value = point.Value * costRate / 1_000_000
			}
			stored, err := c.Store.SaveLLMMetricPointsIfCurrent(ctx, generation, "estimated_cost_"+name, cfg.PodUsernameRegex, cfg.PathMatcher, costPoints)
			if err != nil {
				c.Logger.Warn("llm usage cost save failed", "metric", name, "error", err)
			} else if !stored {
				c.Logger.Debug("discarded stale llm usage cost response", "metric", name)
				break
			}
		}
	}
}

func (c *Collector) featureEnabled(ctx context.Context, feature string) bool {
	var features map[string]json.RawMessage
	if c.Store.GetSetting(ctx, "features", &features) != nil {
		return false
	}
	var enabled bool
	raw, exists := features[feature]
	return exists && json.Unmarshal(raw, &enabled) == nil && enabled
}

func llmEvaluationTime(now time.Time) time.Time {
	return now.UTC().Truncate(time.Minute)
}

func (c *Collector) llmScanDue(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastLLMScan.IsZero() && now.Sub(c.lastLLMScan) < time.Minute {
		return false
	}
	c.lastLLMScan = now
	return true
}

// pruneDue reports whether retention pruning should run now and records the
// attempt. A cycle that ends in pruneFailed is retried after
// pruneRetryInterval instead of the full pruneInterval, so one transient
// database error cannot stall retention for an entire day.
func (c *Collector) pruneDue(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := pruneInterval
	if c.pruneRetry {
		wait = pruneRetryInterval
	}
	if !c.lastPrune.IsZero() && now.Sub(c.lastPrune) < wait {
		return false
	}
	c.lastPrune = now
	c.pruneRetry = false
	return true
}

func (c *Collector) pruneFailed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneRetry = true
}

// retentionReadable reports whether a retention setting read produced a usable
// answer. A missing row means the operator never overrode the built-in
// retention, but any other error leaves the configured window unknown.
func retentionReadable(err error) bool {
	return err == nil || errors.Is(err, store.ErrNotFound)
}

func (c *Collector) prune(ctx context.Context) {
	if !c.pruneDue(time.Now()) {
		return
	}
	var system struct {
		RawRetentionDays int `json:"raw_retention_days"`
	}
	var llm struct {
		RetentionDays int `json:"retention_days"`
	}
	// PruneMetrics falls back to 30 days for an unset window. Applying that
	// fallback because the settings read itself failed would delete samples an
	// operator asked to keep longer, so leave the data alone and retry.
	systemErr := c.Store.GetSetting(ctx, "system", &system)
	llmErr := c.Store.GetSetting(ctx, "llm_usage", &llm)
	if !retentionReadable(systemErr) || !retentionReadable(llmErr) {
		c.Logger.Warn("metric retention settings unreadable", "system_error", systemErr, "llm_error", llmErr)
		c.pruneFailed()
		return
	}
	if err := c.Store.PruneMetrics(ctx, system.RawRetentionDays, llm.RetentionDays); err != nil {
		c.Logger.Warn("metric retention prune failed", "error", err)
		c.pruneFailed()
	}
}

func normalizeLLMLabels(points []integration.MetricPoint, mappings map[string]string) []integration.MetricPoint {
	for index := range points {
		raw := points[index].Labels
		safe := make(map[string]string, 6)
		for _, canonical := range []string{"pod", "path", "status", "model", "hub", "network"} {
			if value, ok := raw[canonical]; ok {
				safe[canonical] = value
			}
		}
		for canonical, remote := range mappings {
			if integration.ValidateLLMLabelMapping(canonical, remote) != nil {
				continue
			}
			if value, ok := raw[remote]; ok {
				safe[canonical] = value
			}
		}
		points[index].Labels = safe
	}
	return points
}
