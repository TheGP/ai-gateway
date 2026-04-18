package router

import (
	"encoding/json"
	"fmt"
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

// SnapshotRouter returns the router's live counters as JSON.
// Used by SaveFullState to combine with account state.
func (r *Router) SnapshotRouter() (json.RawMessage, error) {
	r.mu.Lock()
	snap := persistedRouter{
		TotalRequests:  r.totalRequests,
		TotalSuccess:   r.totalSuccess,
		TotalFailed:    r.totalFailed,
		RecentRequests: append([]RequestLog(nil), r.recentRequests...),
	}
	r.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// SaveFullState atomically writes both account and router state.
// Replaces the old separate SaveState + SaveRouterState calls.
func (r *Router) SaveFullState(path string, accounts []*provider.Account) error {
	routerJSON, err := r.SnapshotRouter()
	if err != nil {
		return fmt.Errorf("snapshot router: %w", err)
	}
	return provider.SaveFullState(accounts, routerJSON, path)
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
