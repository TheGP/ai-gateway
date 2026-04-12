package router

import (
	"encoding/json"
	"os"

	"ai-gateway/logger"
	"ai-gateway/provider"
)

// persistedRouter holds the router-level stats that survive a restart.
type persistedRouter struct {
	TotalRequests  int64        `json:"total_requests"`
	TotalSuccess   int64        `json:"total_success"`
	TotalFailed    int64        `json:"total_failed"`
	RecentRequests []RequestLog `json:"recent_requests"`
}

// SaveRouterState merges the router's live counters into the shared state file,
// preserving the "accounts" section written by provider.SaveState.
func (r *Router) SaveRouterState(path string) error {
	provider.FileMu.Lock()
	defer provider.FileMu.Unlock()

	r.mu.Lock()
	snap := persistedRouter{
		TotalRequests:  r.totalRequests,
		TotalSuccess:   r.totalSuccess,
		TotalFailed:    r.totalFailed,
		RecentRequests: append([]RequestLog(nil), r.recentRequests...),
	}
	r.mu.Unlock()

	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return err
	}

	// Read existing envelope so we don't clobber the accounts section
	state := provider.GatewayState{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	state.Router = json.RawMessage(snapBytes)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadRouterState restores total counters and the recent request log from disk.
func (r *Router) LoadRouterState(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state provider.GatewayState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Router == nil {
		return nil
	}

	var snap persistedRouter
	if err := json.Unmarshal(state.Router, &snap); err != nil {
		return err
	}

	r.mu.Lock()
	r.totalRequests = snap.TotalRequests
	r.totalSuccess = snap.TotalSuccess
	r.totalFailed = snap.TotalFailed
	if len(snap.RecentRequests) > 0 {
		r.recentRequests = snap.RecentRequests
	}
	r.mu.Unlock()

	logger.Info().
		Int64("total_requests", snap.TotalRequests).
		Int("recent_logs", len(snap.RecentRequests)).
		Str("path", path).
		Msg("Router state restored from disk")

	return nil
}
