package provider

import (
	"ai-gateway/config"
	"sync"
	"time"
)

// AccountUsage tracks multi-dimensional rate limit counters per account
type AccountUsage struct {
	// RPM: sliding window of request timestamps
	requestTimes []time.Time

	// RPS: last request time for per-second enforcement
	lastRequestTime time.Time

	// RPD: daily request counter
	dailyRequests  int
	dailyResetTime time.Time

	// TPM: tokens in current minute window
	tokensThisMinute  int
	minuteWindowStart time.Time

	// TPD: daily token counter
	dailyTokens    int

	// Monthly: token budget (Mistral)
	monthlyTokens  int64
	monthResetTime time.Time

	// Reactive cooldown from 429
	cooldownUntil time.Time

	// Stats
	TotalRequests    int64
	TotalTokens      int64
	TotalErrors      int64
	Consecutive429s  int

	mu sync.Mutex
}

func NewAccountUsage() *AccountUsage {
	now := time.Now()
	return &AccountUsage{
		dailyResetTime:    nextMidnightUTC(now),
		minuteWindowStart: now,
		monthResetTime:    nextMonthStart(now),
	}
}

// EstimateTokens gives a rough token count for pre-check purposes
func EstimateTokens(messages []Message) int {
	chars := 0
	for _, m := range messages {
		chars += len(m.Role) + len(m.Content) + 4 // +4 for message overhead
	}
	estimate := chars / 4
	return int(float64(estimate) * 1.1) // 10% safety buffer
}

// CanAccept checks if this account can handle a request without exceeding limits
func (u *AccountUsage) CanAccept(estimatedTokens int, limits config.ModelLimits) bool {
	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()

	// Check cooldown
	if now.Before(u.cooldownUntil) {
		return false
	}

	// Auto-reset daily counters
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

	// RPM check: count requests in last 60 seconds
	if limits.RPM > 0 {
		count := u.requestsInWindow(now, time.Minute)
		if count >= limits.RPM {
			return false
		}
	}

	// RPS check: ensure minimum gap between requests
	if limits.RPS > 0 {
		minGap := time.Second / time.Duration(limits.RPS)
		if now.Sub(u.lastRequestTime) < minGap {
			return false
		}
	}

	// RPD check
	if limits.RPD > 0 && u.dailyRequests >= limits.RPD {
		return false
	}

	// TPM check
	if limits.TPM > 0 && u.tokensThisMinute+estimatedTokens > limits.TPM {
		return false
	}

	// TPD check
	if limits.TPD > 0 && u.dailyTokens+estimatedTokens > limits.TPD {
		return false
	}

	// Monthly check
	if limits.TokensPerMonth > 0 && u.monthlyTokens+int64(estimatedTokens) > limits.TokensPerMonth {
		return false
	}

	return true
}

// RecordRequest updates counters after a successful API response
func (u *AccountUsage) RecordRequest(promptTokens, completionTokens int) {
	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()
	totalTokens := promptTokens + completionTokens

	// RPM tracking
	u.requestTimes = append(u.requestTimes, now)
	u.lastRequestTime = now

	// Daily
	u.dailyRequests++
	u.dailyTokens += totalTokens

	// Minute window
	if now.Sub(u.minuteWindowStart) >= time.Minute {
		u.tokensThisMinute = 0
		u.minuteWindowStart = now
	}
	u.tokensThisMinute += totalTokens

	// Monthly
	u.monthlyTokens += int64(totalTokens)

	// Stats
	u.TotalRequests++
	u.TotalTokens += int64(totalTokens)
	u.Consecutive429s = 0
}

// RecordError records an error (non-429)
func (u *AccountUsage) RecordError() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.TotalErrors++
}

// Record429 records a 429 and sets cooldown
func (u *AccountUsage) Record429(cooldown time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Consecutive429s++
	u.TotalErrors++
	u.cooldownUntil = time.Now().Add(cooldown)
}

// SetCooldown sets a reactive cooldown
func (u *AccountUsage) SetCooldown(d time.Duration) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cooldownUntil = time.Now().Add(d)
}

// IsAvailable returns true if no cooldown is active
func (u *AccountUsage) IsAvailable() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return time.Now().After(u.cooldownUntil)
}

// GetCooldownRemaining returns remaining cooldown time
func (u *AccountUsage) GetCooldownRemaining() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	remaining := time.Until(u.cooldownUntil)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetStats returns a snapshot for the dashboard
type UsageStats struct {
	RPMUsed          int   `json:"rpm_used"`
	RPDUsed          int   `json:"rpd_used"`
	TPMUsed          int   `json:"tpm_used"`
	TPDUsed          int   `json:"tpd_used"`
	MonthlyUsed      int64 `json:"monthly_used"`
	TotalRequests    int64 `json:"total_requests"`
	TotalTokens      int64 `json:"total_tokens"`
	TotalErrors      int64 `json:"total_errors"`
	Consecutive429s  int   `json:"consecutive_429s"`
	CooldownSeconds  int   `json:"cooldown_remaining_s"`
}

func (u *AccountUsage) GetStats() UsageStats {
	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()
	cooldown := time.Until(u.cooldownUntil)
	if cooldown < 0 {
		cooldown = 0
	}

	return UsageStats{
		RPMUsed:         u.requestsInWindow(now, time.Minute),
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
}

// CapacityPercent returns the highest usage percentage across all dimensions
func (u *AccountUsage) CapacityPercent(limits config.ModelLimits) float64 {
	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()
	maxPct := 0.0

	if limits.RPM > 0 {
		pct := float64(u.requestsInWindow(now, time.Minute)) / float64(limits.RPM) * 100
		if pct > maxPct {
			maxPct = pct
		}
	}
	if limits.RPD > 0 {
		pct := float64(u.dailyRequests) / float64(limits.RPD) * 100
		if pct > maxPct {
			maxPct = pct
		}
	}
	if limits.TPD > 0 {
		pct := float64(u.dailyTokens) / float64(limits.TPD) * 100
		if pct > maxPct {
			maxPct = pct
		}
	}
	return maxPct
}

// requestsInWindow counts requests in the last `window` duration
func (u *AccountUsage) requestsInWindow(now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	count := 0
	// Clean old entries while counting
	newTimes := make([]time.Time, 0, len(u.requestTimes))
	for _, t := range u.requestTimes {
		if t.After(cutoff) {
			count++
			newTimes = append(newTimes, t)
		}
	}
	u.requestTimes = newTimes
	return count
}

func nextMidnightUTC(now time.Time) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return next
}

func nextMonthStart(now time.Time) time.Time {
	if now.Month() == 12 {
		return time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}
