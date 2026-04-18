package proxy

import (
	"ai-gateway/logger"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// WebshareProvider fetches proxies from the Webshare API
type WebshareProvider struct {
	apiKey     string
	tracker    *Tracker
	proxies    []webshareProxy
	proxiesMux sync.RWMutex
	lastFetch  time.Time
	refreshing atomic.Bool // prevents duplicate concurrent fetches
}

type webshareProxyResponse struct {
	Count   int              `json:"count"`
	Results []webshareProxy  `json:"results"`
}

type webshareProxy struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	ProxyAddress string `json:"proxy_address"`
	Port         int    `json:"port"`
	CountryCode  string `json:"country_code"`
	Valid        bool   `json:"valid"`
}

func NewWebshareProvider(apiKey string, tracker *Tracker) *WebshareProvider {
	return &WebshareProvider{
		apiKey:  apiKey,
		tracker: tracker,
	}
}

func (w *WebshareProvider) Init() error {
	if w.apiKey == "" {
		return fmt.Errorf("WEBSHARE_API_KEY is not set")
	}
	if err := w.fetchProxies(); err != nil {
		return fmt.Errorf("failed to fetch proxies: %w", err)
	}
	logger.Info().Int("count", len(w.proxies)).Msg("Webshare proxies loaded")
	return nil
}

func (w *WebshareProvider) fetchProxies() error {
	req, err := http.NewRequest("GET", "https://proxy.webshare.io/api/v2/proxy/list/?mode=direct&page=1&page_size=100", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+w.apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webshare API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var proxyResp webshareProxyResponse
	if err := json.Unmarshal(body, &proxyResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	euCountries := map[string]bool{
		"GB": true, "FR": true, "DE": true, "IT": true, "ES": true, "NL": true,
		"BE": true, "AT": true, "SE": true, "NO": true, "DK": true, "FI": true,
		"PL": true, "CZ": true, "IE": true, "PT": true, "GR": true, "CH": true,
	}

	var filtered []webshareProxy
	for _, p := range proxyResp.Results {
		if p.Valid && (p.CountryCode == "US" || euCountries[p.CountryCode]) {
			filtered = append(filtered, p)
		}
	}

	w.proxiesMux.Lock()
	w.proxies = filtered
	w.lastFetch = time.Now()
	w.proxiesMux.Unlock()

	return nil
}

func (w *WebshareProvider) GetProxy(accountKey, provider string) (*ProxyInfo, error) {
	// Check if we already have a saved IP for this account
	if savedIP, ok := w.tracker.GetIP(accountKey); ok {
		p := w.findByIP(savedIP)
		if p != nil {
			return p, nil
		}
		logger.Warn().Str("account", accountKey).Str("ip", savedIP).Msg("Saved proxy IP not found, assigning new one")
	}

	// Refresh if stale, but prevent duplicate concurrent fetches
	w.proxiesMux.RLock()
	stale := time.Since(w.lastFetch) > 5*time.Minute
	w.proxiesMux.RUnlock()

	if stale && w.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer w.refreshing.Store(false)
			if err := w.fetchProxies(); err != nil {
				logger.Warn().Err(err).Msg("Background proxy refresh failed")
			} else {
				logger.Debug().Msg("Background proxy refresh succeeded")
			}
		}()
	}

	// Find unused proxy
	w.proxiesMux.RLock()
	defer w.proxiesMux.RUnlock()

	if len(w.proxies) == 0 {
		return nil, fmt.Errorf("no proxies available")
	}

	// Try random proxies until we find one not reserved by this provider
	for attempts := 0; attempts < len(w.proxies)*2; attempts++ {
		idx := rand.Intn(len(w.proxies))
		p := w.proxies[idx]
		if !w.tracker.IsIPUsed(p.ProxyAddress, provider) {
			info := &ProxyInfo{
				Address:  fmt.Sprintf("%s:%d", p.ProxyAddress, p.Port),
				Username: p.Username,
				Password: p.Password,
				Protocol: "socks5",
			}
			if err := w.tracker.SetIP(accountKey, p.ProxyAddress, provider); err != nil {
				logger.Warn().Err(err).Msg("Failed to save IP mapping")
			}
			logger.Info().Str("account", accountKey).Str("ip", p.ProxyAddress).Str("country", p.CountryCode).Msg("Assigned proxy")
			return info, nil
		}
	}

	return nil, fmt.Errorf("no unused proxies available")
}

// ReleaseProxy keeps the IP reserved for 1 month (soft release) so it
// won't be assigned to another account on the same provider.
func (w *WebshareProvider) ReleaseProxy(accountKey, provider string) error {
	return w.tracker.SoftReleaseIP(accountKey)
}

func (w *WebshareProvider) findByIP(ip string) *ProxyInfo {
	w.proxiesMux.RLock()
	defer w.proxiesMux.RUnlock()
	for _, p := range w.proxies {
		if p.ProxyAddress == ip {
			return &ProxyInfo{
				Address:  fmt.Sprintf("%s:%d", p.ProxyAddress, p.Port),
				Username: p.Username,
				Password: p.Password,
				Protocol: "socks5",
			}
		}
	}
	return nil
}

const proxyReservationTTL = 30 * 24 * time.Hour // 1 month

// Tracker persists API key → proxy IP mappings with per-provider isolation.
// Once assigned, a proxy IP is reserved for the provider for 1 month so
// different accounts on the same provider cannot reuse the same IP.
type Tracker struct {
	filePath string
	// mappings: apiKey → ipEntry (active assignments)
	mappings map[string]ipEntry
	// reservations: "provider:ip" → expiry (includes active + soft-released)
	reservations map[string]time.Time
	mu           sync.RWMutex
}

type ipEntry struct {
	IP         string
	Provider   string
	AssignedAt time.Time
}

type ipMapping struct {
	APIKey     string    `json:"api_key"`
	IP         string    `json:"ip"`
	Provider   string    `json:"provider"`
	AssignedAt time.Time `json:"assigned_at"`
}

func NewTracker(filePath string) *Tracker {
	return &Tracker{
		filePath:     filePath,
		mappings:     make(map[string]ipEntry),
		reservations: make(map[string]time.Time),
	}
}

func (t *Tracker) Load() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read IP tracker: %w", err)
	}

	var mappings []ipMapping
	if err := json.Unmarshal(data, &mappings); err != nil {
		return fmt.Errorf("failed to parse IP tracker: %w", err)
	}

	now := time.Now()
	pruned := 0
	for _, m := range mappings {
		assignedAt := m.AssignedAt
		if assignedAt.IsZero() {
			// Legacy entry without timestamp — treat as assigned now so it
			// gets a full 1-month window from this load.
			assignedAt = now
		}
		expiry := assignedAt.Add(proxyReservationTTL)
		if now.After(expiry) {
			pruned++
			continue // expired, drop it
		}
		entry := ipEntry{IP: m.IP, Provider: m.Provider, AssignedAt: assignedAt}
		t.mappings[m.APIKey] = entry
		t.reservations[reservationKey(m.Provider, m.IP)] = expiry
	}
	if pruned > 0 {
		_ = t.save() // persist pruned state
	}
	return nil
}

func (t *Tracker) save() error {
	var mappings []ipMapping
	for key, e := range t.mappings {
		mappings = append(mappings, ipMapping{
			APIKey:     key,
			IP:         e.IP,
			Provider:   e.Provider,
			AssignedAt: e.AssignedAt,
		})
	}
	// Also persist soft-released reservations (no active account key)
	// by keeping them in mappings under a synthetic key.
	// We store them as entries with APIKey = "" — but that would collide.
	// Instead we only persist active mappings; reservations are reconstructed
	// from those on Load. Soft-released IPs are persisted via the mappings
	// map under a synthetic "_released:<provider>:<ip>" key.
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func reservationKey(provider, ip string) string {
	return provider + ":" + ip
}

func (t *Tracker) GetIP(apiKey string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if e, ok := t.mappings[apiKey]; ok {
		return e.IP, true
	}
	return "", false
}

func (t *Tracker) SetIP(apiKey, ip, provider string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	expiry := now.Add(proxyReservationTTL)
	t.mappings[apiKey] = ipEntry{IP: ip, Provider: provider, AssignedAt: now}
	t.reservations[reservationKey(provider, ip)] = expiry
	return t.save()
}

// IsIPUsed reports whether ip is currently reserved by the given provider.
func (t *Tracker) IsIPUsed(ip, provider string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	expiry, ok := t.reservations[reservationKey(provider, ip)]
	if !ok {
		return false
	}
	return time.Now().Before(expiry)
}

// SoftReleaseIP keeps the IP reserved for the provider for 1 month from the
// original assignment date, but removes the active account → IP mapping so
// the account can be reassigned a new proxy if needed.
func (t *Tracker) SoftReleaseIP(apiKey string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.mappings[apiKey]; ok {
		// Keep the reservation in t.reservations (it already has the expiry).
		// Persist it under a synthetic key so it survives restarts.
		syntheticKey := "_released:" + e.Provider + ":" + e.IP
		t.mappings[syntheticKey] = e
		delete(t.mappings, apiKey)
	}
	return t.save()
}
