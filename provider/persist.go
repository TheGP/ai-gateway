package provider

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"ai-gateway/logger"
)

// persistedUsage is the JSON-serializable subset of AccountUsage.
// Short-lived windows (requestTimes, tokensThisMinute) are excluded —
// they are meaningless across restarts.
type persistedUsage struct {
	DailyRequests  int       `json:"daily_requests"`
	DailyTokens    int       `json:"daily_tokens"`
	DailyResetTime time.Time `json:"daily_reset_time"`
	MonthlyTokens  int64     `json:"monthly_tokens"`
	MonthResetTime time.Time `json:"month_reset_time"`
	CooldownUntil  time.Time `json:"cooldown_until"`
	TotalRequests  int64     `json:"total_requests"`
	TotalTokens    int64     `json:"total_tokens"`
	TotalErrors    int64     `json:"total_errors"`
	Consecutive429s int      `json:"consecutive_429s"`
}

// GatewayState is the shared top-level JSON envelope written to gateway_state.json.
// Each package reads and writes only its own field; the rest is preserved as raw JSON.
type GatewayState struct {
	Accounts map[string]persistedUsage `json:"accounts"`
	Router   json.RawMessage           `json:"router,omitempty"` // owned by router package
}

// FileMu is exported so the router package can co-ordinate on the same mutex,
// preventing concurrent writes to gateway_state.json from the two packages.
var FileMu sync.Mutex

func accountKey(a *Account) string { return a.DisplayName() }

// SaveState serialises account usage into the "accounts" section of path,
// preserving any other sections (e.g. "router") that are already on disk.
func SaveState(accounts []*Account, path string) error {
	FileMu.Lock()
	defer FileMu.Unlock()

	// Read existing state so we don't clobber the router section
	state := GatewayState{Accounts: make(map[string]persistedUsage, len(accounts))}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]persistedUsage, len(accounts))
	}

	for _, a := range accounts {
		a.Usage.mu.Lock()
		state.Accounts[accountKey(a)] = persistedUsage{
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
		}
		a.Usage.mu.Unlock()
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
