package main

// runSync compares live provider model lists against providers.yaml.
// Called when the binary is invoked with --sync. Uses the fully-initialised
// accounts (including Webshare proxy info) that the gateway already builds,
// so every request goes through the same proxy as the real traffic.
//
// Usage:
//
//	go run . --sync

import (
	"ai-gateway/config"
	"ai-gateway/provider"
	"ai-gateway/proxy"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ── ANSI colours ──────────────────────────────────────────────────────────────

const (
	syncRed   = "\033[31m"
	syncGreen = "\033[32m"
	syncCyan  = "\033[36m"
	syncBold  = "\033[1m"
	syncDim   = "\033[2m"
	syncReset = "\033[0m"
)

func syncClr(color, s string) string {
	if runtime.GOOS == "windows" && os.Getenv("TERM") == "" && os.Getenv("WT_SESSION") == "" {
		return s
	}
	return color + s + syncReset
}

// ── API response shapes ───────────────────────────────────────────────────────

type syncOpenAIResp struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type syncGeminiResp struct {
	Models []struct {
		Name string `json:"name"` // "models/gemini-2.5-flash"
	} `json:"models"`
}

type syncOpenRouterResp struct {
	Data []struct {
		ID      string `json:"id"`
		Pricing struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		} `json:"pricing"`
	} `json:"data"`
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

func syncGetJSON(client *http.Client, url, bearerToken string, dest interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "curl/7.88.1")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if strings.HasPrefix(msg, "<!DOCTYPE") || strings.Contains(msg, "Cloudflare") {
			return fmt.Errorf("HTTP %d: blocked by Cloudflare (IP-based restriction)", resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(body, dest)
}

func syncFetchOpenAI(client *http.Client, baseURL, apiKey string) ([]string, error) {
	var resp syncOpenAIResp
	if err := syncGetJSON(client, baseURL+"/models", apiKey, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

func syncFetchGemini(client *http.Client, baseURL, apiKey string) ([]string, error) {
	var resp syncGeminiResp
	url := baseURL + "/models?key=" + apiKey
	if err := syncGetJSON(client, url, "", &resp); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.Models))
	for _, m := range resp.Models {
		ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
	}
	return ids, nil
}

func syncFetchOpenRouter(client *http.Client, apiKey string) ([]string, error) {
	var resp syncOpenRouterResp
	if err := syncGetJSON(client, "https://openrouter.ai/api/v1/models", apiKey, &resp); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range resp.Data {
		if m.Pricing.Prompt == "0" && m.Pricing.Completion == "0" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ── Diff output ───────────────────────────────────────────────────────────────

func syncPrintSection(title string, items []string, color, symbol string) {
	if len(items) == 0 {
		return
	}
	sep := strings.Repeat("━", 62)
	fmt.Println(syncClr(color, sep))
	fmt.Println(syncClr(syncBold, syncClr(color, "  "+symbol+"  "+title)))
	fmt.Println(syncClr(color, sep))
	sort.Strings(items)
	for _, id := range items {
		fmt.Printf("     %s\n", syncClr(color, id))
	}
	fmt.Println()
}

func syncPrintDiff(name string, yamlModels []config.ModelConfig, liveIDs []string, anyChange *bool) {
	yamlSet := map[string]bool{}
	for _, m := range yamlModels {
		yamlSet[m.ID] = true
	}
	liveSet := map[string]bool{}
	for _, id := range liveIDs {
		liveSet[id] = true
	}

	var discontinued, toAdd []string
	for id := range yamlSet {
		if !liveSet[id] {
			discontinued = append(discontinued, id)
		}
	}
	for id := range liveSet {
		if !yamlSet[id] {
			toAdd = append(toAdd, id)
		}
	}

	header := fmt.Sprintf("  %s  (%d configured, %d live)",
		strings.ToUpper(name), len(yamlModels), len(liveIDs))
	fmt.Println(syncClr(syncBold, syncClr(syncCyan, header)))

	if len(discontinued) == 0 && len(toAdd) == 0 {
		fmt.Printf("  %s all configured models still available\n\n", syncClr(syncGreen, "✓"))
		return
	}

	*anyChange = true
	syncPrintSection("Discontinued — remove from providers.yaml", discontinued, syncRed, "✗")
	syncPrintSection("Available to add — not yet in providers.yaml", toAdd, syncGreen, "✚")
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runSync(cfg *config.Config, accounts []*provider.Account) {
	// Pick the first account per provider (it already has ProxyInfo set)
	firstAccount := map[string]*provider.Account{}
	for _, acc := range accounts {
		if _, exists := firstAccount[acc.ProviderName]; !exists {
			firstAccount[acc.ProviderName] = acc
		}
	}

	fmt.Println()
	fmt.Printf("%s\n\n", syncClr(syncBold, "Fetching live model lists from providers…"))

	anyChange := false

	for _, p := range cfg.Providers {
		acc := firstAccount[p.Name]
		if acc == nil {
			fmt.Printf("%s  %s — no active account, skipping\n\n",
				syncClr(syncDim, "○"), syncClr(syncBold, p.Name))
			continue
		}

		// Build an HTTP client using the account's assigned proxy (same as gateway)
		client, err := proxy.MakeHTTPClient(acc.ProxyInfo, 0)
		if err != nil {
			fmt.Printf("%s  %s — proxy error: %v\n\n",
				syncClr(syncRed, "✗"), syncClr(syncBold, p.Name), err)
			continue
		}
		client.Timeout = 20 * time.Second

		var liveIDs []string
		switch p.Type {
		case "gemini":
			liveIDs, err = syncFetchGemini(client, p.BaseURL, acc.APIKey)
		default:
			liveIDs, err = syncFetchOpenAI(client, p.BaseURL, acc.APIKey)
		}
		if err != nil {
			fmt.Printf("%s  %s — fetch error: %v\n\n",
				syncClr(syncRed, "✗"), syncClr(syncBold, p.Name), err)
			continue
		}

		syncPrintDiff(p.Name, p.Models, liveIDs, &anyChange)
	}

	// OpenRouter — separate since it's not a configured provider
	orKey := os.Getenv("OPENROUTER_API_KEY")
	if orKey == "" {
		fmt.Printf("%s  %s — set OPENROUTER_API_KEY in .env to enable\n\n",
			syncClr(syncDim, "○"), syncClr(syncBold, "openrouter"))
	} else {
		// OpenRouter doesn't need a proxy; use a plain client
		plainClient := &http.Client{Timeout: 20 * time.Second}
		freeIDs, err := syncFetchOpenRouter(plainClient, orKey)
		if err != nil {
			fmt.Printf("%s  %s — fetch error: %v\n\n",
				syncClr(syncRed, "✗"), syncClr(syncBold, "openrouter"), err)
		} else {
			syncPrintDiff("openrouter (free models only)", nil, freeIDs, &anyChange)
		}
	}

	if !anyChange {
		fmt.Println(syncClr(syncGreen, "✓ providers.yaml is up to date with all provider APIs."))
	}
	fmt.Println()
}
