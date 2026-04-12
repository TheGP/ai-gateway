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
	"time"
)

// WebshareProvider fetches proxies from the Webshare API
type WebshareProvider struct {
	apiKey     string
	tracker    *Tracker
	proxies    []webshareProxy
	proxiesMux sync.RWMutex
	lastFetch  time.Time
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

func (w *WebshareProvider) GetProxy(accountKey string) (*ProxyInfo, error) {
	// Check if we already have a saved IP for this account
	if savedIP, ok := w.tracker.GetIP(accountKey); ok {
		p := w.findByIP(savedIP)
		if p != nil {
			return p, nil
		}
		logger.Warn().Str("account", accountKey).Str("ip", savedIP).Msg("Saved proxy IP not found, assigning new one")
	}

	// Refresh if stale
	w.proxiesMux.RLock()
	if time.Since(w.lastFetch) > 5*time.Minute {
		w.proxiesMux.RUnlock()
		go w.fetchProxies()
	} else {
		w.proxiesMux.RUnlock()
	}

	// Find unused proxy
	w.proxiesMux.RLock()
	defer w.proxiesMux.RUnlock()

	if len(w.proxies) == 0 {
		return nil, fmt.Errorf("no proxies available")
	}

	// Try random proxies until we find one not already used
	for attempts := 0; attempts < len(w.proxies)*2; attempts++ {
		idx := rand.Intn(len(w.proxies))
		p := w.proxies[idx]
		if !w.tracker.IsIPUsed(p.ProxyAddress) {
			info := &ProxyInfo{
				Address:  fmt.Sprintf("%s:%d", p.ProxyAddress, p.Port),
				Username: p.Username,
				Password: p.Password,
				Protocol: "socks5",
			}
			if err := w.tracker.SetIP(accountKey, p.ProxyAddress); err != nil {
				logger.Warn().Err(err).Msg("Failed to save IP mapping")
			}
			logger.Info().Str("account", accountKey).Str("ip", p.ProxyAddress).Str("country", p.CountryCode).Msg("Assigned proxy")
			return info, nil
		}
	}

	return nil, fmt.Errorf("no unused proxies available")
}

func (w *WebshareProvider) ReleaseProxy(accountKey string) error {
	return w.tracker.ReleaseIP(accountKey)
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

// Tracker persists API key → proxy IP mappings
type Tracker struct {
	filePath string
	mappings map[string]string
	usedIPs  map[string]bool
	mu       sync.RWMutex
}

type ipMapping struct {
	APIKey string `json:"api_key"`
	IP     string `json:"ip"`
}

func NewTracker(filePath string) *Tracker {
	return &Tracker{
		filePath: filePath,
		mappings: make(map[string]string),
		usedIPs:  make(map[string]bool),
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

	for _, m := range mappings {
		t.mappings[m.APIKey] = m.IP
		t.usedIPs[m.IP] = true
	}
	return nil
}

func (t *Tracker) save() error {
	var mappings []ipMapping
	for key, ip := range t.mappings {
		mappings = append(mappings, ipMapping{APIKey: key, IP: ip})
	}
	data, err := json.MarshalIndent(mappings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(t.filePath, data, 0644)
}

func (t *Tracker) GetIP(apiKey string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ip, ok := t.mappings[apiKey]
	return ip, ok
}

func (t *Tracker) SetIP(apiKey, ip string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mappings[apiKey] = ip
	t.usedIPs[ip] = true
	return t.save()
}

func (t *Tracker) IsIPUsed(ip string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.usedIPs[ip]
}

func (t *Tracker) ReleaseIP(apiKey string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ip, ok := t.mappings[apiKey]; ok {
		delete(t.usedIPs, ip)
		delete(t.mappings, apiKey)
	}
	return t.save()
}
