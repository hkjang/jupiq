package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hkjang/jupiq/internal/integration"
	"github.com/hkjang/jupiq/internal/store"
)

type Collector struct {
	Store       *store.Store
	Logger      *slog.Logger
	mu          sync.Mutex
	last        map[int64]time.Time
	lastLLMScan time.Time
	lastPrune   time.Time
}

func New(s *store.Store, logger *slog.Logger) *Collector {
	return &Collector{Store: s, Logger: logger, last: map[int64]time.Time{}}
}

func (c *Collector) Run(ctx context.Context) {
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
		go func() {
			requestCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			token, err := c.Store.HubToken(requestCtx, hub.ID)
			if err != nil {
				_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, false, "", err.Error(), nil)
				return
			}
			client, err := integration.NewJupyterHub(hub.BaseURL, token, hub.VerifyTLS)
			if err != nil {
				_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, false, "", err.Error(), nil)
				return
			}
			info, err := client.Info(requestCtx)
			if err != nil {
				_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, false, "", err.Error(), nil)
				return
			}
			users, err := client.Users(requestCtx)
			if err != nil {
				_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, false, info.Version, err.Error(), nil)
				return
			}
			if err := c.Store.SyncHubUsers(requestCtx, hub, users); err != nil {
				_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, false, info.Version, err.Error(), nil)
				return
			}
			_ = c.Store.UpdateHubHealth(requestCtx, hub.ID, true, info.Version, "", map[string]any{"users": len(users), "synced_at": time.Now().UTC()})
		}()
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

func (c *Collector) collectPrometheus(ctx context.Context) {
	var cfg prometheusConfig
	if c.Store.GetSetting(ctx, "prometheus", &cfg) != nil || !cfg.Enabled || cfg.BaseURL == "" {
		return
	}
	if cfg.Queries == nil {
		cfg.Queries = map[string]string{
			"cpu_cores":    `sum(rate(container_cpu_usage_seconds_total{pod=~"jupyter-.*"}[5m])) by (pod)`,
			"memory_bytes": `sum(container_memory_working_set_bytes{pod=~"jupyter-.*"}) by (pod)`,
		}
	}
	var features struct {
		GPUMonitoring bool `json:"gpu_monitoring"`
	}
	_ = c.Store.GetSetting(ctx, "features", &features)
	if features.GPUMonitoring {
		if _, ok := cfg.Queries["gpu_utilization"]; !ok {
			cfg.Queries["gpu_utilization"] = `avg(DCGM_FI_DEV_GPU_UTIL) by (pod)`
		}
		if _, ok := cfg.Queries["vram_bytes"]; !ok {
			cfg.Queries["vram_bytes"] = `sum(DCGM_FI_DEV_FB_USED * 1024 * 1024) by (pod)`
		}
	}
	token, _ := c.Store.GetSecret(ctx, "prometheus.token")
	client, err := integration.NewPrometheus(cfg.BaseURL, token, cfg.VerifyTLS)
	if err != nil {
		c.Logger.Warn("prometheus config invalid", "error", err)
		return
	}
	for name, query := range cfg.Queries {
		if !features.GPUMonitoring && (name == "gpu_utilization" || name == "vram_bytes" || name == "gpu_count") {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		points, err := client.Query(requestCtx, query, time.Now())
		cancel()
		if err != nil {
			c.Logger.Warn("prometheus query failed", "metric", name, "error", err)
			continue
		}
		if err := c.Store.SaveMetricPoints(ctx, "prometheus", name, points); err != nil {
			c.Logger.Warn("save metric failed", "metric", name, "error", err)
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
	var cfg kubernetesConfig
	if c.Store.GetSetting(ctx, "kubernetes", &cfg) != nil || !cfg.Enabled || cfg.BaseURL == "" {
		return
	}
	token, _ := c.Store.GetSecret(ctx, "kubernetes.token")
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
		_ = c.Store.GetSetting(ctx, "llm_usage", &llm)
		cfg.PodUsernameRegex = llm.PodUsernameRegex
	}
	if err := c.Store.UpdatePods(ctx, pods, cfg.PodUsernameRegex); err != nil {
		c.Logger.Warn("kubernetes pod save failed", "error", err)
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
	var features struct {
		Enabled bool `json:"llm_usage_monitoring"`
	}
	_ = c.Store.GetSetting(ctx, "features", &features)
	if !features.Enabled {
		return
	}
	// The default query uses increase(...[1m]). Querying it on the 30-second
	// base ticker would count overlapping windows twice, so LLM counters have
	// their own non-overlapping one-minute cadence.
	if !c.llmScanDue(time.Now()) {
		return
	}
	var cfg llmUsageConfig
	if c.Store.GetSetting(ctx, "llm_usage", &cfg) != nil || cfg.Source != "prometheus" {
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
	if c.Store.GetSetting(ctx, "prometheus", &prom) != nil || !prom.Enabled || prom.BaseURL == "" {
		return
	}
	token, _ := c.Store.GetSecret(ctx, "prometheus.token")
	client, err := integration.NewPrometheus(prom.BaseURL, token, prom.VerifyTLS)
	if err != nil {
		return
	}
	queries := cfg.PromQL
	if len(queries) == 0 {
		queries = map[string]string{"calls": `sum(increase(http_requests_total{path="/v1/chat/completions"}[1m])) by (pod,path,status,model,hub)`}
	}
	for name, query := range queries {
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		points, queryErr := client.Query(requestCtx, query, time.Now())
		cancel()
		if queryErr != nil {
			c.Logger.Warn("llm usage query failed", "metric", name, "error", queryErr)
			continue
		}
		points = normalizeLLMLabels(points, cfg.LabelMappings)
		if err := c.Store.SaveLLMMetricPoints(ctx, name, cfg.PodUsernameRegex, cfg.PathMatcher, points); err != nil {
			c.Logger.Warn("llm usage save failed", "metric", name, "error", err)
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
			if err := c.Store.SaveLLMMetricPoints(ctx, "estimated_cost", cfg.PodUsernameRegex, cfg.PathMatcher, costPoints); err != nil {
				c.Logger.Warn("llm usage cost save failed", "metric", name, "error", err)
			}
		}
	}
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

func (c *Collector) prune(ctx context.Context) {
	now := time.Now()
	c.mu.Lock()
	if !c.lastPrune.IsZero() && now.Sub(c.lastPrune) < 24*time.Hour {
		c.mu.Unlock()
		return
	}
	c.lastPrune = now
	c.mu.Unlock()
	var system struct {
		RawRetentionDays int `json:"raw_retention_days"`
	}
	var llm struct {
		RetentionDays int `json:"retention_days"`
	}
	_ = c.Store.GetSetting(ctx, "system", &system)
	_ = c.Store.GetSetting(ctx, "llm_usage", &llm)
	if err := c.Store.PruneMetrics(ctx, system.RawRetentionDays, llm.RetentionDays); err != nil {
		c.Logger.Warn("metric retention prune failed", "error", err)
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
