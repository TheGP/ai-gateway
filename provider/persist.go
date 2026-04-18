package provider

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"ai-gateway/logger"
)

// persistedModelUsage holds the durable (cross-restart) subset of ModelUsage.
// Short-lived windows (requestTimes, tokensThisMinute) are excluded.
type persistedModelUsage struct {
	DailyRequests  int       `json:"daily_requests"`
	DailyTokens    int       `json:"daily_tokens"`
	DailyResetTime time.Time `json:"daily_reset_time"`
	MonthlyTokens  int64     `json:"monthly_tokens"`
	MonthResetTime time.Time `json:"month_reset_time"`
}

// persistedUsage is the JSON-serializable subset of AccountUsage.
type persistedUsage struct {
	// Account-level shared counters (per_account / both modes)
	DailyRequests  int       `json:"daily_requests"`
	DailyTokens    int       `json:"daily_tokens"`
	DailyResetTime time.Time `json:"daily_reset_time"`
	MonthlyTokens  int64     `json:"monthly_tokens"`
	MonthResetTime time.Time `json:"month_reset_time"`

	// Reactive cooldown + lifetime stats (always)
	CooldownUntil   time.Time `json:"cooldown_until"`
	TotalRequests   int64     `json:"total_requests"`
	TotalTokens     int64     `json:"total_tokens"`
	TotalErrors     int64     `json:"total_errors"`
	Consecutive429s int       `json:"consecutive_429s"`

	// persisted so dead keys aren't retried after restart
	Disabled bool `json:"disabled,omitempty"`

	// Per-model state (per_model / both modes)
	ModelUsage map[string]persistedModelUsage `json:"model_usage,omitempty"`
}

// GatewayState is the shared top-level JSON envelope written to gateway_state.json.
type GatewayState struct {
	Accounts map[string]persistedUsage `json:"accounts"`
	Router   json.RawMessage           `json:"router,omitempty"`
}

// FileMu is exported so the router package can co-ordinate on the same mutex.
var FileMu sync.Mutex

func accountKey(a *Account) string { return a.DisplayName() }

// SnapshotAccounts serialises account usage into a persistedUsage map.
// It does NOT write to disk — the caller combines this with router state
// and writes atomically.
func SnapshotAccounts(accounts []*Account) map[string]persistedUsage {
	result := make(map[string]persistedUsage, len(accounts))
	for _, a := range accounts {
		a.Usage.mu.Lock()
		p := persistedUsage{
			DailyRequests:   a.Usage.dailyRequests,
			DailyTokens:     a.Usage.dailyTokens,
			DailyResetTime:  a.Usage.dailyResetTime,
			MonthlyTokens:   a.Usage.monthlyTokens,
			MonthResetTime:  a.Usage.monthResetTime,
			CooldownUntil:   a.Usage.cooldownUntil,
			TotalRequests:   a.Usage.TotalRequests,
			TotalTokens:     a.Usage.TotalTokens,
			TotalErrors:     a.Usage.TotalErrors,
			Consecutive429s: a.Usage.Consecutive429s,
			Disabled:        a.IsDisabled(),
		}
		// Persist per-model state
		if len(a.Usage.modelUsage) > 0 {
			p.ModelUsage = make(map[string]persistedModelUsage, len(a.Usage.modelUsage))
			for id, m := range a.Usage.modelUsage {
				m.mu.Lock()
				p.ModelUsage[id] = persistedModelUsage{
					DailyRequests:  m.dailyRequests,
					DailyTokens:    m.dailyTokens,
					DailyResetTime: m.dailyResetTime,
					MonthlyTokens:  m.monthlyTokens,
					MonthResetTime: m.monthResetTime,
				}
				m.mu.Unlock()
			}
		}
		a.Usage.mu.Unlock()
		result[accountKey(a)] = p
	}
	return result
}

// SaveFullState atomically writes both account and router state to path.
func SaveFullState(accounts []*Account, routerJSON json.RawMessage, path string) error {
	FileMu.Lock()
	defer FileMu.Unlock()

	state := GatewayState{
		Accounts: SnapshotAccounts(accounts),
		Router:   routerJSON,
	}
	return writeAtomic(path, state)
}

// LoadState restores account usage from the "accounts" section of path.
func LoadState(accounts []*Account, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state GatewayState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	restored := 0
	for _, a := range accounts {
		s, ok := state.Accounts[accountKey(a)]
		if !ok {
			continue
		}

		a.Usage.mu.Lock()
		a.Usage.dailyRequests = s.DailyRequests
		a.Usage.dailyTokens = s.DailyTokens
		a.Usage.dailyResetTime = s.DailyResetTime
		a.Usage.monthlyTokens = s.MonthlyTokens
		a.Usage.monthResetTime = s.MonthResetTime
		a.Usage.cooldownUntil = s.CooldownUntil
		a.Usage.TotalRequests = s.TotalRequests
		a.Usage.TotalTokens = s.TotalTokens
		a.Usage.TotalErrors = s.TotalErrors
		a.Usage.Consecutive429s = s.Consecutive429s
		a.Usage.mu.Unlock()

		// restore disabled state
		if s.Disabled {
			a.SetDisabled(true)
		}

		// Restore per-model state
		for id, pm := range s.ModelUsage {
			m := newModelUsage(a.DailyReset)
			m.dailyRequests = pm.DailyRequests
			m.dailyTokens = pm.DailyTokens
			m.dailyResetTime = pm.DailyResetTime
			m.monthlyTokens = pm.MonthlyTokens
			m.monthResetTime = pm.MonthResetTime

			a.Usage.mu.Lock()
			a.Usage.modelUsage[id] = m
			a.Usage.mu.Unlock()
		}

		restored++
	}

	logger.Info().
		Int("restored", restored).
		Int("total_accounts", len(accounts)).
		Str("path", path).
		Msg("Usage state restored from disk")

	return nil
}

// writeAtomic marshals v and atomically replaces path via a temp file.
func writeAtomic(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
