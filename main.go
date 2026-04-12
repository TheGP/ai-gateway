package main

import (
	"ai-gateway/alerts"
	"ai-gateway/config"
	"ai-gateway/dashboard"
	"ai-gateway/logger"
	"ai-gateway/provider"
	"ai-gateway/proxy"
	"ai-gateway/router"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env (ignore error if not found)
	_ = godotenv.Load()

	logger.Init()

	// Override port from env if set
	portEnv := os.Getenv("GATEWAY_PORT")

	// Load config
	cfg, err := config.Load("providers.yaml")
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to load config")
	}

	if portEnv != "" {
		if p, err := strconv.Atoi(portEnv); err == nil {
			cfg.Gateway.Port = p
		}
	}

	logger.Info().Str("providers", cfg.Summary()).Int("port", cfg.Gateway.Port).Msg("Config loaded")

	// Initialize proxy provider
	var proxyProvider proxy.ProxyProvider
	tracker := proxy.NewTracker(cfg.Proxy.IPMappingsFile)
	if err := tracker.Load(); err != nil {
		logger.Warn().Err(err).Msg("Failed to load IP tracker")
	}

	if cfg.NeedsProxy() {
		switch cfg.Proxy.Type {
		case "webshare":
			proxyProvider = proxy.NewWebshareProvider(cfg.Proxy.APIKey, tracker)
		case "static":
			configs := make([]proxy.StaticConfig, 0, len(cfg.Proxy.Proxies))
			for _, p := range cfg.Proxy.Proxies {
				configs = append(configs, proxy.StaticConfig{
					Address:  p.Address,
					Username: p.Username,
					Password: p.Password,
					Protocol: p.Protocol,
				})
			}
			proxyProvider = proxy.NewStaticProvider(configs, tracker)
		case "none":
			logger.Warn().Msg("Proxy type is 'none' but some accounts have proxy: true")
		}

		if proxyProvider != nil {
			if err := proxyProvider.Init(); err != nil {
				logger.Fatal().Err(err).Msg("Failed to initialize proxy provider")
			}
		}
	}

	// Build accounts
	accounts := buildAccounts(cfg, proxyProvider)
	logger.Info().Int("accounts", len(accounts)).Msg("Accounts initialized")

	// --sync: compare live provider model lists against providers.yaml, then exit
	for _, arg := range os.Args[1:] {
		if arg == "--sync" {
			runSync(cfg, accounts)
			os.Exit(0)
		}
	}

	// Initialize router
	r := router.New(accounts, cfg)

	// Initialize Telegram alerter
	telegram := alerts.NewTelegramAlerter(cfg.Telegram.BotToken, cfg.Telegram.ChatID, cfg.Telegram.AlertCooldown)

	// Initialize dashboard
	dash := dashboard.NewHandler(&statsAdapter{r: r}, cfg.Gateway.AuthToken)

	// HTTP server
	mux := http.NewServeMux()

	// Auth middleware
	authMiddleware := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			auth := req.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || auth[7:] != cfg.Gateway.AuthToken {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"message": "Invalid or missing API key",
						"type":    "authentication_error",
					},
				})
				return
			}
			next(w, req)
		}
	}

	// Routes
	mux.HandleFunc("/v1/chat/completions", authMiddleware(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleChatCompletion(w, req, r, cfg, telegram)
	}))

	mux.HandleFunc("/v1/models", authMiddleware(func(w http.ResponseWriter, req *http.Request) {
		handleListModels(w, req, cfg)
	}))

	mux.HandleFunc("/health", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/dashboard", dash.ServeDashboard)
	mux.HandleFunc("/dashboard/login", dash.ServeLogin)
	mux.HandleFunc("/dashboard/logout", dash.ServeLogout)
	mux.HandleFunc("/api/stats", dash.ServeStats)

	addr := fmt.Sprintf(":%d", cfg.Gateway.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: cfg.Gateway.RequestTimeout + 5*time.Second,
	}

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info().Str("addr", addr).Msg("AI Gateway started")
		logger.Info().Str("dashboard", fmt.Sprintf("http://localhost:%d/dashboard", cfg.Gateway.Port)).Msg("Dashboard URL")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("Server failed")
		}
	}()

	<-done
	logger.Info().Msg("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func buildAccounts(cfg *config.Config, proxyProvider proxy.ProxyProvider) []*provider.Account {
	var accounts []*provider.Account

	for _, p := range cfg.Providers {
		for _, acc := range p.Accounts {
			account := &provider.Account{
				ProviderName:  p.Name,
				ProviderType:  p.Type,
				BaseURL:       p.BaseURL,
				APIKey:        acc.APIKey,
				APIKeyEnv:     acc.APIKeyEnv,
				Models:        p.Models,
				UseProxy:      acc.Proxy,
				ProxyOverride: acc.ProxyOverride,
				DailyReset:    p.DailyReset,
				Usage:         provider.NewAccountUsage(),
			}

			// Set up proxy
			if acc.ProxyOverride != "" {
				info, err := proxy.ParseProxyURL(acc.ProxyOverride)
				if err != nil {
					logger.Warn().Err(err).Str("account", account.DisplayName()).Msg("Failed to parse proxy override")
				} else {
					account.ProxyInfo = info
				}
			} else if acc.Proxy && proxyProvider != nil {
				info, err := proxyProvider.GetProxy(acc.APIKeyEnv)
				if err != nil {
					logger.Warn().Err(err).Str("account", account.DisplayName()).Msg("Failed to get proxy")
				} else {
					account.ProxyInfo = info
				}
			}

			accounts = append(accounts, account)
		}
	}

	return accounts
}

func handleChatCompletion(w http.ResponseWriter, req *http.Request, r *router.Router, cfg *config.Config, telegram *alerts.TelegramAlerter) {
	var chatReq provider.ChatRequest
	if err := json.NewDecoder(req.Body).Decode(&chatReq); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "Invalid request body: " + err.Error(),
				"type":    "invalid_request_error",
			},
		})
		return
	}

	if chatReq.Model == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": "model is required",
				"type":    "invalid_request_error",
			},
		})
		return
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(req.Context(), cfg.Gateway.RequestTimeout)
	defer cancel()

	resp, err := r.Route(ctx, chatReq)
	if err != nil {
		// Send Telegram alert for exhaustion
		telegram.AlertExhausted(chatReq.Model, err.Error())

		status := http.StatusServiceUnavailable
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"message": err.Error(),
				"type":    "rate_limit",
			},
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleListModels(w http.ResponseWriter, req *http.Request, cfg *config.Config) {
	models := cfg.ListModels()

	data := make([]map[string]interface{}, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]interface{}{
			"id":       m,
			"object":   "model",
			"owned_by": "ai-gateway",
		})
	}

	// Add aliases
	for alias, target := range cfg.Aliases {
		data = append(data, map[string]interface{}{
			"id":       alias,
			"object":   "model",
			"owned_by": "ai-gateway",
			"alias_of": target,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   data,
	})
}

// statsAdapter wraps the router to implement dashboard.StatsProvider
type statsAdapter struct {
	r *router.Router
}

func (s *statsAdapter) GetStats() interface{} {
	return s.r.GetStats()
}
