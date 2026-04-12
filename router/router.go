package router

import (
	"ai-gateway/alerts"
	"ai-gateway/config"
	"ai-gateway/logger"
	"ai-gateway/provider"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Router handles intelligent request routing across accounts
type Router struct {
	accounts       []*provider.Account
	cfg            *config.Config
	telegram       *alerts.TelegramAlerter
	requestTimeout time.Duration
	retryDelay     time.Duration

	// Stats
	recentRequests []RequestLog
	alerts         []AlertLog
	startTime      time.Time
	totalRequests  int64
	totalSuccess   int64
	totalFailed    int64
	mu             sync.Mutex
}

type RequestLog struct {
	Time          time.Time `json:"time"`
	RequestedModel string  `json:"requested_model"`
	ActualModel   string   `json:"actual_model"`
	Provider      string   `json:"provider"`
	Account       string   `json:"account"`
	Fallback      bool     `json:"fallback"`
	DurationMs    int64    `json:"duration_ms"`
	Tokens        int      `json:"tokens"`
	Status        string   `json:"status"`
	Error         string   `json:"error,omitempty"`
}

type AlertLog struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

func New(accounts []*provider.Account, cfg *config.Config, telegram *alerts.TelegramAlerter) *Router {
	return &Router{
		accounts:       accounts,
		cfg:            cfg,
		telegram:       telegram,
		requestTimeout: cfg.Gateway.RequestTimeout,
		retryDelay:     cfg.Gateway.RetryDelay,
		startTime:      time.Now(),
	}
}

// Route handles the full routing logic with alias resolution, proactive checking,
// tier fallback, and retry.
func (r *Router) Route(ctx context.Context, req provider.ChatRequest) (*provider.ChatResponse, error) {
	start := time.Now()

	// 1. Resolve alias
	originalModel := req.Model
	req.Model = r.cfg.ResolveAlias(req.Model)

	// 2. Estimate tokens
	estimatedTokens := provider.EstimateTokens(req.Messages)

	// 3. Try the requested model
	resp, account, err := r.tryModel(ctx, req, estimatedTokens)
	if err == nil {
		r.recordSuccess(start, originalModel, req.Model, account, resp, false)
		return r.attachGatewayMeta(resp, originalModel, req.Model, account, false), nil
	}

	// 4. Tier fallback — find models at same or higher tier
	// Skip tier fallback when provider is forced (user wants that specific provider)
	if req.XProvider == "" {
		_, modelCfg := r.cfg.FindModelProvider(req.Model)
		if modelCfg != nil {
			fallbackModels := r.getModelsAtOrAboveTier(modelCfg.Tier, req.Model)
			for _, fallbackModel := range fallbackModels {
				fallbackReq := req
				fallbackReq.Model = fallbackModel
				resp, account, err = r.tryModel(ctx, fallbackReq, estimatedTokens)
				if err == nil {
					logger.Info().Str("original", req.Model).Str("fallback", fallbackModel).Msg("Tier fallback succeeded")
					r.recordSuccess(start, originalModel, fallbackModel, account, resp, true)
					return r.attachGatewayMeta(resp, originalModel, fallbackModel, account, true), nil
				}
			}
		}
	}

	// 5. Wait and retry once
	logger.Warn().Str("model", req.Model).Msg("All accounts exhausted, waiting before retry")
	select {
	case <-time.After(r.retryDelay):
	case <-ctx.Done():
		r.recordFailure(start, originalModel, req.Model, "timeout")
		return nil, ctx.Err()
	}

	resp, account, err = r.tryModel(ctx, req, estimatedTokens)
	if err == nil {
		r.recordSuccess(start, originalModel, req.Model, account, resp, false)
		return r.attachGatewayMeta(resp, originalModel, req.Model, account, false), nil
	}

	// 6. All failed
	r.recordFailure(start, originalModel, req.Model, err.Error())
	r.addAlert("error", fmt.Sprintf("All providers exhausted for model %q", originalModel))

	return nil, fmt.Errorf("all providers exhausted for model %q", originalModel)
}

// tryModel attempts to send a request to any available account for the given model
func (r *Router) tryModel(ctx context.Context, req provider.ChatRequest, estimatedTokens int) (*provider.ChatResponse, *provider.Account, error) {
	var candidates []*provider.Account
	if req.XProvider != "" {
		candidates = r.getAccountsForModelAndProvider(req.Model, req.XProvider)
		if len(candidates) == 0 {
			return nil, nil, fmt.Errorf("no accounts serve model %q on provider %q", req.Model, req.XProvider)
		}
		logger.Debug().Str("provider", req.XProvider).Str("model", req.Model).Int("candidates", len(candidates)).Msg("Forced provider routing")
	} else {
		candidates = r.getAccountsForModel(req.Model)
		if len(candidates) == 0 {
			return nil, nil, fmt.Errorf("no accounts serve model %q", req.Model)
		}
	}

	// Sort by LastUsed (oldest first = round-robin effect)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].LastUsed.Before(candidates[j].LastUsed)
	})

	// unavailableProviders tracks which providers returned 503 for this model.
	// A 503 is infrastructure-level for that provider only — other providers
	// serving the same model (e.g. Bedrock vs Anthropic direct) are independent.
	unavailableProviders := make(map[string]bool)

	var lastErr error
	for _, account := range candidates {
		// Skip accounts that have been permanently disabled (e.g. expired key)
		if account.Disabled {
			logger.Debug().Str("account", account.DisplayName()).Msg("Skipping disabled account")
			continue
		}

		// Skip accounts from providers that already returned 503 for this model
		if unavailableProviders[account.ProviderName] {
			logger.Debug().Str("account", account.DisplayName()).Str("provider", account.ProviderName).Msg("Skipping: provider already returned 503 for this model")
			continue
		}

		limits := account.GetModelLimits(req.Model)

		// Proactive check (per-model and/or per-account depending on limit mode)
		if !account.Usage.CanAccept(estimatedTokens, limits, account.AccountLimits, req.Model, account.LimitMode) {
			logger.Debug().Str("account", account.DisplayName()).Str("model", req.Model).Str("limit_mode", account.LimitMode).Msg("Skipping: proactive limit check failed")
			continue
		}

		// Send request
		account.LastUsed = time.Now()
		resp, err := r.sendToAccount(ctx, account, req)
		if err == nil {
			// Update counters with actual tokens
			account.Usage.RecordRequest(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, req.Model, account.LimitMode)

			// Check capacity and alert
			capacity := account.Usage.CapacityPercent(limits, req.Model, account.LimitMode)
			if capacity > 80 {
				r.addAlert("warning", fmt.Sprintf("Capacity >80%% on %s/%s (%.0f%%)", account.DisplayName(), req.Model, capacity))
			}

			return resp, account, nil
		}

		// Handle model-level unavailability (503) — skip all remaining accounts
		// from this provider but continue to other providers serving the same model.
		// e.g. Anthropic 503 does not mean Bedrock is also overloaded.
		var modelUnavailErr *provider.ModelUnavailableError
		if errors.As(err, &modelUnavailErr) {
			unavailableProviders[account.ProviderName] = true
			logger.Warn().Str("model", req.Model).Str("provider", account.ProviderName).Msg("Model unavailable (503) — skipping remaining accounts for this provider")
			lastErr = err
			continue
		}

		// Handle permanently invalid/expired key — disable and alert
		var invalidKeyErr *provider.InvalidKeyError
		if errors.As(err, &invalidKeyErr) {
			account.Disabled = true
			logger.Error().Str("account", account.DisplayName()).Msg("API key invalid/expired — account disabled until restart")
			r.addAlert("error", fmt.Sprintf("Dead API key: %s — disabled until restart", account.DisplayName()))
			r.telegram.AlertInvalidKey(account.DisplayName())
			lastErr = err
			continue
		}

		// Handle 429
		var rateLimitErr *provider.RateLimitError
		if errors.As(err, &rateLimitErr) {
			account.Usage.Record429(rateLimitErr.RetryAfter)
			logger.Warn().Str("account", account.DisplayName()).Str("model", req.Model).Dur("cooldown", rateLimitErr.RetryAfter).Msg("Rate limited (429)")

			if account.Usage.GetStats(account.LimitMode, account.Models).Consecutive429s >= 5 {
				r.addAlert("warning", fmt.Sprintf("Consecutive 429s on %s (%d)", account.DisplayName(), account.Usage.GetStats(account.LimitMode, account.Models).Consecutive429s))
			}
		} else {
			account.Usage.RecordError()
			logger.Warn().Err(err).Str("account", account.DisplayName()).Str("model", req.Model).Msg("Request failed")
		}

		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no available accounts for model %q", req.Model)
	}
	return nil, nil, lastErr
}

func (r *Router) sendToAccount(ctx context.Context, account *provider.Account, req provider.ChatRequest) (*provider.ChatResponse, error) {
	switch account.ProviderType {
	case "gemini":
		return provider.GeminiSend(ctx, account, req)
	case "openai":
		return provider.OpenAISend(ctx, account, req)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", account.ProviderType)
	}
}

func (r *Router) getAccountsForModel(modelID string) []*provider.Account {
	var result []*provider.Account
	for _, a := range r.accounts {
		if a.SupportsModel(modelID) {
			result = append(result, a)
		}
	}
	return result
}

// getAccountsForModelAndProvider filters accounts by both model and provider name
func (r *Router) getAccountsForModelAndProvider(modelID, providerName string) []*provider.Account {
	var result []*provider.Account
	for _, a := range r.accounts {
		if a.ProviderName == providerName && a.SupportsModel(modelID) {
			result = append(result, a)
		}
	}
	return result
}

func (r *Router) getModelsAtOrAboveTier(minTier int, excludeModel string) []string {
	type modelInfo struct {
		id   string
		tier int
	}
	var candidates []modelInfo

	seen := make(map[string]bool)
	for _, p := range r.cfg.Providers {
		if len(p.Accounts) == 0 {
			continue
		}
		for _, m := range p.Models {
			if m.ID != excludeModel && m.Tier >= minTier && !seen[m.ID] {
				candidates = append(candidates, modelInfo{id: m.ID, tier: m.Tier})
				seen[m.ID] = true
			}
		}
	}

	// Sort by tier ascending (try same-tier first, then higher)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].tier < candidates[j].tier
	})

	result := make([]string, 0, len(candidates))
	for _, c := range candidates {
		result = append(result, c.id)
	}
	return result
}

func (r *Router) attachGatewayMeta(resp *provider.ChatResponse, originalModel, actualModel string, account *provider.Account, isFallback bool) *provider.ChatResponse {
	resp.XGateway = &provider.GatewayMetadata{
		Provider: account.ProviderName,
		Account:  account.APIKeyEnv,
		Fallback: isFallback,
	}
	if isFallback || originalModel != actualModel {
		resp.XGateway.OriginalModel = originalModel
	}
	resp.Model = actualModel
	return resp
}

func (r *Router) recordSuccess(start time.Time, originalModel, actualModel string, account *provider.Account, resp *provider.ChatResponse, isFallback bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalRequests++
	r.totalSuccess++
	r.recentRequests = append(r.recentRequests, RequestLog{
		Time:           time.Now(),
		RequestedModel: originalModel,
		ActualModel:    actualModel,
		Provider:       account.ProviderName,
		Account:        account.APIKeyEnv,
		Fallback:       isFallback,
		DurationMs:     time.Since(start).Milliseconds(),
		Tokens:         resp.Usage.TotalTokens,
		Status:         "ok",
	})
	r.trimRecentRequests()
}

func (r *Router) recordFailure(start time.Time, originalModel, actualModel, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalRequests++
	r.totalFailed++
	r.recentRequests = append(r.recentRequests, RequestLog{
		Time:           time.Now(),
		RequestedModel: originalModel,
		ActualModel:    actualModel,
		DurationMs:     time.Since(start).Milliseconds(),
		Status:         "error",
		Error:          errMsg,
	})
	r.trimRecentRequests()
}

func (r *Router) addAlert(level, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.alerts = append(r.alerts, AlertLog{
		Time:    time.Now(),
		Level:   level,
		Message: message,
	})
	// Keep last 100 alerts
	if len(r.alerts) > 100 {
		r.alerts = r.alerts[len(r.alerts)-100:]
	}
}

func (r *Router) trimRecentRequests() {
	if len(r.recentRequests) > 100 {
		r.recentRequests = r.recentRequests[len(r.recentRequests)-100:]
	}
}

// GetStats returns stats for the dashboard
type Stats struct {
	Uptime         string        `json:"uptime"`
	TotalRequests  int64         `json:"total_requests"`
	Successful     int64         `json:"successful"`
	Failed         int64         `json:"failed"`
	Accounts       []AccountStat `json:"accounts"`
	RecentRequests []RequestLog  `json:"recent_requests"`
	Alerts         []AlertLog    `json:"alerts"`
}

type AccountStat struct {
	Provider  string               `json:"provider"`
	Account   string               `json:"account"`
	Models    []string             `json:"models"`
	LimitMode string               `json:"limit_mode"`
	Status    string               `json:"status"`
	Usage     provider.UsageStats  `json:"usage"`
	Limits    config.ModelLimits   `json:"limits"`
}

func (r *Router) GetStats() Stats {
	r.mu.Lock()
	recentCopy := make([]RequestLog, len(r.recentRequests))
	copy(recentCopy, r.recentRequests)
	alertsCopy := make([]AlertLog, len(r.alerts))
	copy(alertsCopy, r.alerts)
	total := r.totalRequests
	success := r.totalSuccess
	failed := r.totalFailed
	r.mu.Unlock()

	uptime := time.Since(r.startTime).Round(time.Second)

	accountStats := make([]AccountStat, 0, len(r.accounts))
	for _, a := range r.accounts {
		models := make([]string, 0, len(a.Models))
		for _, m := range a.Models {
			models = append(models, m.ID)
		}

		status := "ok"
		usage := a.Usage.GetStats(a.LimitMode, a.Models)
		if usage.CooldownSeconds > 0 {
			status = "cooldown"
		}

		// Use limits from first model as representative for account-level display
		var limits config.ModelLimits
		if len(a.Models) > 0 {
			limits = a.Models[0].Limits
		}

		accountStats = append(accountStats, AccountStat{
			Provider:  a.ProviderName,
			Account:   a.APIKeyEnv,
			Models:    models,
			LimitMode: a.LimitMode,
			Status:    status,
			Usage:     usage,
			Limits:    limits,
		})
	}

	// Reverse recent requests (newest first)
	for i, j := 0, len(recentCopy)-1; i < j; i, j = i+1, j-1 {
		recentCopy[i], recentCopy[j] = recentCopy[j], recentCopy[i]
	}

	return Stats{
		Uptime:         formatDuration(uptime),
		TotalRequests:  total,
		Successful:     success,
		Failed:         failed,
		Accounts:       accountStats,
		RecentRequests: recentCopy,
		Alerts:         alertsCopy,
	}
}

// GetAlerts returns pending alerts to send via Telegram
func (r *Router) GetPendingAlerts() []AlertLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]AlertLog, len(r.alerts))
	copy(result, r.alerts)
	return result
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
