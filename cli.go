package main

// cli.go — CLI subcommand helpers (accounts, enable, disable).
// Called from main() when a known subcommand is detected; exits immediately after.

import (
	"ai-gateway/config"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func gatewayBase(cfg *config.Config) string {
	return fmt.Sprintf("http://localhost:%d", cfg.Gateway.Port)
}

// runAccounts prints all account statuses by calling /api/stats.
func runAccounts(cfg *config.Config) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", gatewayBase(cfg)+"/api/stats", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Gateway.AuthToken)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌  Cannot reach gateway: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var stats struct {
		Accounts []struct {
			Provider string `json:"provider"`
			Account  string `json:"account"`
			Status   string `json:"status"`
			Usage    struct {
				TotalRequests   int64 `json:"total_requests"`
				TotalErrors     int64 `json:"total_errors"`
				CooldownSeconds int   `json:"cooldown_remaining_s"`
			} `json:"usage"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		fmt.Fprintf(os.Stderr, "❌  Bad response: %v\n", err)
		os.Exit(1)
	}

	maxLen := 7 // min width for "Account"
	for _, a := range stats.Accounts {
		if n := len(a.Provider + "/" + a.Account); n > maxLen {
			maxLen = n
		}
	}
	fmt.Printf("\n%-*s  %-14s  %8s  %6s  %s\n", maxLen, "Account", "Status", "Requests", "Errors", "Cooldown")
	fmt.Println(strings.Repeat("─", maxLen+42))
	for _, a := range stats.Accounts {
		key := a.Provider + "/" + a.Account
		icon := map[string]string{
			"ok":       "✅ ok",
			"cooldown": "🟡 cooldown",
			"disabled": "🔴 disabled",
		}[a.Status]
		if icon == "" {
			icon = a.Status
		}
		cd := "—"
		if a.Usage.CooldownSeconds > 0 {
			cd = fmt.Sprintf("%ds", a.Usage.CooldownSeconds)
		}
		fmt.Printf("%-*s  %-14s  %8d  %6d  %s\n",
			maxLen, key, icon,
			a.Usage.TotalRequests, a.Usage.TotalErrors, cd)
	}
	fmt.Println()
}

// runSetDisabled enables or disables an account via the admin API.
func runSetDisabled(cfg *config.Config, account string, disabled bool) {
	body, _ := json.Marshal(map[string]interface{}{
		"account":  account,
		"disabled": disabled,
	})
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", gatewayBase(cfg)+"/api/accounts/set-disabled",
		strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+cfg.Gateway.AuthToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌  Cannot reach gateway: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "❌  %s\n", result["error"])
		os.Exit(1)
	}
	icon := "✅"
	action := "enabled"
	if disabled {
		icon = "🔴"
		action = "disabled"
	}
	fmt.Printf("%s  %s → %s\n", icon, account, action)
}
