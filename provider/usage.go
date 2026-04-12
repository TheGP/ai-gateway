package provider

import (
	"ai-gateway/config"
	"sync"
	"time"
)

// ─── ModelUsage ────────────────────────────────────────────────────────────
// Tracks rate-limit counters for a single model ID independently.
// Used by "per_model" and "both" limit modes.

type ModelUsage struct {
	// RPM: sliding window of request timestamps
	requestTimes []time.Time

	// RPS: last request time for per-second enforcement
	lastRequestTime time.Time

	// TPM: tokens in current minute window
	tokensThisMinute  int
	minuteWindowStart time.Time

	// RPD / TPD: daily counters
	dailyRequests  int
	dailyResetTime time.Time
	dailyTokens    int

	// Monthly token budget (e.g. mistral-large tokens_per_month)
	monthlyTokens  int64
	monthResetTime time.Time

	mu sync.Mutex
}

func newModelUsage() *ModelUsage {
	now := time.Now()
	return &ModelUsage{
		dailyResetTime:    nextMidnightUTC(now),
		minuteWindowStart: now,
		monthResetTime:    nextMonthStart(now),
	}
}

// canAccept checks per-model limits only.
func (m *ModelUsage) canAccept(estimatedTokens int, limits config.ModelLimits) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// Auto-reset daily counters
	if now.After(m.dailyResetTime) {
		m.dailyRequests = 0
		m.dailyTokens = 0
		m.dailyResetTime = nextMidnightUTC(now)
	}

	// Auto-reset minute window
	if now.Sub(m.minuteWindowStart) >= time.Minute {
		m.tokensThisMinute = 0
		m.minuteWindowStart = now
	}

	// Auto-reset monthly
	if now.After(m.monthResetTime) {
		m.monthlyTokens = 0
		m.monthResetTime = nextMonthStart(now)
	}

	// RPM check
	if limits.RPM > 0 {
		if modelRequestsInWindow(m.requestTimes, now, time.Minute) >= limits.RPM {
			return false
		}
	}

	// RPS check (per-model RPS, e.g. individual model throttle)
	if limits.RPS > 0 {
		minGap := time.Duration(float64(time.Second) / float64(limits.RPS))
		if now.Sub(m.lastRequestTime) < minGap {
			return false
		}
	}

	// RPD check
	if limits.RPD > 0 && m.dailyRequests >= limits.RPD {
		return false
	}

	// TPM check
	if limits.TPM > 0 && m.tokensThisMinute+estimatedTokens > limits.TPM {
		return false
	}

	// TPD check
	if limits.TPD > 0 && m.dailyTokens+estimatedTokens > limits.TPD {
		return false
	}

	// Monthly check
	if limits.TokensPerMonth > 0 && m.monthlyTokens+int64(estimatedTokens) > limits.TokensPerMonth {
		return false
	}

	return true
}

// record updates per-model counters after a successful request.
func (m *ModelUsage) record(promptTokens, completionTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	total := promptTokens + completionTokens

	m.requestTimes = append(m.requestTimes, now)
	m.lastRequestTime = now
	m.dailyRequests++
	m.dailyTokens += total

	if now.Sub(m.minuteWindowStart) >= time.Minute {
		m.tokensThisMinute = 0
		m.minuteWindowStart = now
	}
	m.tokensThisMinute += total
	m.monthlyTokens += int64(total)
}

// capacityPercent returns highest usage % across RPM/RPD/TPD for dashboard display.
func (m *ModelUsage) capacityPercent(limits config.ModelLimits) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	max := 0.0

	if limits.RPM > 0 {
		pct := float64(modelRequestsInWindow(m.requestTimes, now, time.Minute)) / float64(limits.RPM) * 100
		if pct > max {
			max = pct
		}
	}
	if limits.RPD > 0 {
		pct := float64(m.dailyRequests) / float64(limits.RPD) * 100
		if pct > max {
			max = pct
		}
	}
	if limits.TPD > 0 {
		pct := float64(m.dailyTokens) / float64(limits.TPD) * 100
		if pct > max {
			max = pct
		}
	}
	return max
}

func (m *ModelUsage) getStats(limits config.ModelLimits) ModelUsageStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	return ModelUsageStats{
		RPMUsed:      modelRequestsInWindow(m.requestTimes, now, time.Minute),
		RPDUsed:      m.dailyRequests,
		TPMUsed:      m.tokensThisMinute,
		TPDUsed:      m.dailyTokens,
		MonthlyUsed:  m.monthlyTokens,
		RPMLimit:     limits.RPM,
		RPDLimit:     limits.RPD,
		TPMLimit:     limits.TPM,
		TPDLimit:     limits.TPD,
		MonthlyLimit: limits.TokensPerMonth,
	}
}

// ModelUsageStats is the dashboard-visible snapshot for a single model.
type ModelUsageStats struct {
	RPMUsed      int   `json:"rpm_used"`
	RPDUsed      int   `json:"rpd_used"`
	TPMUsed      int   `json:"tpm_used"`
	TPDUsed      int   `json:"tpd_used"`
	MonthlyUsed  int64 `json:"monthly_used"`
	RPMLimit     int   `json:"rpm_limit"`
	RPDLimit     int   `json:"rpd_limit"`
	TPMLimit     int   `json:"tpm_limit"`
	TPDLimit     int   `json:"tpd_limit"`
	MonthlyLimit int64 `json:"monthly_limit"`
}

// ─── AccountUsage ──────────────────────────────────────────────────────────
// Tracks account-level state: reactive cooldown, lifetime stats, and shared
// counters for "per_account" and "both" modes.

type AccountUsage struct {
	// Per-model buckets (used in "per_model" and "both" modes)
	modelUsage map[string]*ModelUsage

	// Shared account-level counters (used in "per_account" and "both" modes)
	requestTimes      []time.Time
	lastRequestTime   time.Time
	tokensThisMinute  int
	minuteWindowStart time.Time
	dailyRequests     int
	dailyResetTime    time.Time
	dailyTokens       int
	monthlyTokens     int64
	monthResetTime    time.Time

	// Reactive 429 cooldown — always account-level
	cooldownUntil time.Time

	// Lifetime stats — always account-level, shown in dashboard
	TotalRequests   int64
	TotalTokens     int64
	TotalErrors     int64
	Consecutive429s int

	mu sync.Mutex
}

func NewAccountUsage() *AccountUsage {
	now := time.Now()
	return &AccountUsage{
		modelUsage:        make(map[string]*ModelUsage),
		dailyResetTime:    nextMidnightUTC(now),
		minuteWindowStart: now,
		monthResetTime:    nextMonthStart(now),
	}
}

// forModel returns (or lazily creates) the ModelUsage for a given model ID.
// Caller must NOT hold u.mu when calling this.
func (u *AccountUsage) forModel(modelID string) *ModelUsage {
	u.mu.Lock()
	m, ok := u.modelUsage[modelID]
	if !ok {
		m = newModelUsage()
		u.modelUsage[modelID] = m
	}
	u.mu.Unlock()
	return m
}

// EstimateTokens gives a rough token count for pre-check purposes.
func EstimateTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Role) + len(m.Content) + 4
	}
	estimate := chars / 4
	return int(float64(estimate) * 1.1)
}

// CanAccept checks if this account can handle a request.
// limitMode: "per_model" | "per_account" | "both"
func (u *AccountUsage) CanAccept(estimatedTokens int, limits config.ModelLimits, accountLimits config.ModelLimits, modelID, limitMode string) bool {
	u.mu.Lock()
	now := time.Now()

	// Always check cooldown first
	if now.Before(u.cooldownUntil) {
		u.mu.Unlock()
		return false
	}

	// Check shared account-level counters for "per_account" and "both" modes
	if limitMode == "per_account" || limitMode == "both" {
		// Which limits to use: for "per_account" the model limits ARE the account limits;
		// for "both" we use the explicit accountLimits (e.g. rps: 1 for Mistral)
		checkLimits := limits
		if limitMode == "both" {
			checkLimits = accountLimits
		}

		// Auto-reset daily
		if now.After(u.dailyResetTime) {
			u.dailyRequests = 0
			u.dailyTokens = 0
			u.dailyResetTime = nextMidnightUTC(now)
		}
		// Auto-reset minute window
		if now.Sub(u.minuteWindowStart) >= time.Minute {
			u.tokensThisMinute = 0
			u.minuteWindowStart = now
		}
		// Auto-reset monthly
		if now.After(u.monthResetTime) {
			u.monthlyTokens = 0
			u.monthResetTime = nextMonthStart(now)
		}

		if checkLimits.RPM > 0 && requestsInWindow(u.requestTimes, now, time.Minute) >= checkLimits.RPM {
			u.mu.Unlock()
			return false
		}
		if checkLimits.RPS > 0 {
			minGap := time.Duration(float64(time.Second) / float64(checkLimits.RPS))
			if now.Sub(u.lastRequestTime) < minGap {
				u.mu.Unlock()
				return false
			}
		}
		if checkLimits.RPD > 0 && u.dailyRequests >= checkLimits.RPD {
			u.mu.Unlock()
			return false
		}
		if checkLimits.TPM > 0 && u.tokensThisMinute+estimatedTokens > checkLimits.TPM {
			u.mu.Unlock()
			return false
		}
		if checkLimits.TPD > 0 && u.dailyTokens+estimatedTokens > checkLimits.TPD {
			u.mu.Unlock()
			return false
		}
		if checkLimits.TokensPerMonth > 0 && u.monthlyTokens+int64(estimatedTokens) > checkLimits.TokensPerMonth {
			u.mu.Unlock()
			return false
		}
	}
	u.mu.Unlock()

	// Check per-model counters for "per_model" and "both" modes
	if limitMode == "per_model" || limitMode == "both" {
		m := u.forModel(modelID)
		if !m.canAccept(estimatedTokens, limits) {
			return false
		}
	}

	return true
}

// RecordRequest updates counters after a successful API response.
func (u *AccountUsage) RecordRequest(promptTokens, completionTokens int, modelID, limitMode string) {
	total := promptTokens + completionTokens
	now := time.Now()

	// Update account-level shared counters
	if limitMode == "per_account" || limitMode == "both" {
		u.mu.Lock()
		u.requestTimes = append(u.requestTimes, now)
		u.lastRequestTime = now
		u.dailyRequests++
		u.dailyTokens += total
		if now.Sub(u.minuteWindowStart) >= time.Minute {
			u.tokensThisMinute = 0
			u.minuteWindowStart = now
		}
		u.tokensThisMinute += total
		u.monthlyTokens += int64(total)
		u.mu.Unlock()
	}

	// Update per-model counters
	if limitMode == "per_model" || limitMode == "both" {
		m := u.forModel(modelID)
		m.record(promptTokens, completionTokens)
	}

	// Always update lifetime stats
	u.mu.Lock()
	u.TotalRequests++
	u.TotalTokens += int64(total)
	u.Consecutive429s = 0
	u.mu.Unlock()
}

// RecordError records a non-429 error.
func (u *AccountUsage) RecordError() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.TotalErrors++
}

// Record429 records a 429 and sets a cooldown on the whole account.
func (u *AccountUsage) Record429(cooldown time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Consecutive429s++
	u.TotalErrors++
	u.cooldownUntil = time.Now().Add(cooldown)
}

// SetCooldown sets a reactive cooldown.
func (u *AccountUsage) SetCooldown(d time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cooldownUntil = time.Now().Add(d)
}

// IsAvailable returns true if no cooldown is active.
func (u *AccountUsage) IsAvailable() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return time.Now().After(u.cooldownUntil)
}

// GetCooldownRemaining returns remaining cooldown time.
func (u *AccountUsage) GetCooldownRemaining() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	remaining := time.Until(u.cooldownUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CapacityPercent returns the highest usage % across all dimensions for the given model.
func (u *AccountUsage) CapacityPercent(limits config.ModelLimits, modelID, limitMode string) float64 {
	if limitMode == "per_model" || limitMode == "both" {
		m := u.forModel(modelID)
		return m.capacityPercent(limits)
	}

	// per_account
	u.mu.Lock()
	defer u.mu.Unlock()
	now := time.Now()
	max := 0.0
	if limits.RPM > 0 {
		pct := float64(requestsInWindow(u.requestTimes, now, time.Minute)) / float64(limits.RPM) * 100
		if pct > max {
			max = pct
		}
	}
	if limits.RPD > 0 {
		pct := float64(u.dailyRequests) / float64(limits.RPD) * 100
		if pct > max {
			max = pct
		}
	}
	if limits.TPD > 0 {
		pct := float64(u.dailyTokens) / float64(limits.TPD) * 100
		if pct > max {
			max = pct
		}
	}
	return max
}

// UsageStats is the dashboard-visible snapshot for an account.
type UsageStats struct {
	RPMUsed         int                        `json:"rpm_used"`
	RPDUsed         int                        `json:"rpd_used"`
	TPMUsed         int                        `json:"tpm_used"`
	TPDUsed         int                        `json:"tpd_used"`
	MonthlyUsed     int64                      `json:"monthly_used"`
	TotalRequests   int64                      `json:"total_requests"`
	TotalTokens     int64                      `json:"total_tokens"`
	TotalErrors     int64                      `json:"total_errors"`
	Consecutive429s int                        `json:"consecutive_429s"`
	CooldownSeconds int                        `json:"cooldown_remaining_s"`
	ModelStats      map[string]ModelUsageStats `json:"model_stats,omitempty"`
}

func (u *AccountUsage) GetStats(limitMode string, modelLimits []config.ModelConfig) UsageStats {
	u.mu.Lock()
	now := time.Now()
	cooldown := time.Until(u.cooldownUntil)
	if cooldown < 0 {
		cooldown = 0
	}
	stats := UsageStats{
		RPMUsed:         requestsInWindow(u.requestTimes, now, time.Minute),
		RPDUsed:         u.dailyRequests,
		TPMUsed:         u.tokensThisMinute,
		TPDUsed:         u.dailyTokens,
		MonthlyUsed:     u.monthlyTokens,
		TotalRequests:   u.TotalRequests,
		TotalTokens:     u.TotalTokens,
		TotalErrors:     u.TotalErrors,
		Consecutive429s: u.Consecutive429s,
		CooldownSeconds: int(cooldown.Seconds()),
	}
	u.mu.Unlock()

	// Attach per-model stats for per_model and both modes.
	// Always include every configured model — defaults to all-zero if no requests yet.
	if limitMode == "per_model" || limitMode == "both" {
		stats.ModelStats = make(map[string]ModelUsageStats, len(modelLimits))
		for _, mc := range modelLimits {
			u.mu.Lock()
			m, ok := u.modelUsage[mc.ID]
			u.mu.Unlock()
			if ok {
				stats.ModelStats[mc.ID] = m.getStats(mc.Limits)
			} else {
				// No requests yet — emit zero stats with limits so the dashboard can render bars
				stats.ModelStats[mc.ID] = ModelUsageStats{
					RPMLimit:     mc.Limits.RPM,
					RPDLimit:     mc.Limits.RPD,
					TPMLimit:     mc.Limits.TPM,
					TPDLimit:     mc.Limits.TPD,
					MonthlyLimit: mc.Limits.TokensPerMonth,
				}
			}
		}
	}

	return stats
}

// ─── helpers ───────────────────────────────────────────────────────────────

func requestsInWindow(times []time.Time, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	count := 0
	newTimes := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(cutoff) {
			count++
			newTimes = append(newTimes, t)
		}
	}
	// Note: we don't mutate the slice here since caller may not hold the right lock.
	// Trimming happens in RecordRequest instead.
	return count
}

// modelRequestsInWindow is the same but operates on a ModelUsage slice (caller holds m.mu).
func modelRequestsInWindow(times []time.Time, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	count := 0
	newTimes := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			count++
			newTimes = append(newTimes, t)
		}
	}
	return count
}

func nextMidnightUTC(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

func nextMonthStart(now time.Time) time.Time {
	if now.Month() == 12 {
		return time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}
