package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration
type Config struct {
	Gateway   GatewayConfig          `yaml:"gateway"`
	Proxy     ProxyConfig            `yaml:"proxy"`
	Telegram  TelegramConfig         `yaml:"telegram"`
	Aliases   map[string]string      `yaml:"aliases"`
	Providers []ProviderConfig       `yaml:"providers"`
}

type GatewayConfig struct {
	Port           int           `yaml:"port"`
	AuthTokenEnv   string        `yaml:"auth_token_env"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	RetryDelay     time.Duration `yaml:"retry_delay"`

	// Resolved
	AuthToken string `yaml:"-"`
}

type ProxyConfig struct {
	Type           string              `yaml:"type"` // "webshare", "static", "none"
	APIKeyEnv      string              `yaml:"api_key_env"`
	IPMappingsFile string              `yaml:"ip_mappings_file"`
	Proxies        []StaticProxyConfig `yaml:"proxies"`

	// Resolved
	APIKey string `yaml:"-"`
}

type StaticProxyConfig struct {
	Address  string `yaml:"address"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Protocol string `yaml:"protocol"` // "socks5" or "http"
}

type TelegramConfig struct {
	BotTokenEnv    string        `yaml:"bot_token_env"`
	ChatIDEnv      string        `yaml:"chat_id_env"`
	AlertCooldown  time.Duration `yaml:"alert_cooldown"`

	// Resolved
	BotToken string `yaml:"-"`
	ChatID   string `yaml:"-"`
}

type ProviderConfig struct {
	Name          string          `yaml:"name"`
	Type          string          `yaml:"type"` // "gemini" or "openai"
	BaseURL       string          `yaml:"base_url"`
	DailyReset    string          `yaml:"daily_reset"`
	LimitMode     string          `yaml:"limit_mode"`     // "per_model" (default) | "per_account" | "both"
	AccountLimits ModelLimits     `yaml:"account_limits"` // shared limits across all models (used in "both" mode)
	Models        []ModelConfig   `yaml:"models"`
	Accounts      []AccountConfig `yaml:"accounts"`
}

type ModelConfig struct {
	ID            string      `yaml:"id"`
	Tier          int         `yaml:"tier"`
	ContextWindow int         `yaml:"context_window"`
	Limits        ModelLimits `yaml:"limits"`
}

type ModelLimits struct {
	RPM            int   `yaml:"rpm" json:"rpm"`
	RPD            int   `yaml:"rpd" json:"rpd"`
	RPS            int   `yaml:"rps" json:"rps"`
	TPM            int   `yaml:"tpm" json:"tpm"`
	TPD            int   `yaml:"tpd" json:"tpd"`
	TokensPerMonth int64 `yaml:"tokens_per_month" json:"tokens_per_month"`
}

type AccountConfig struct {
	APIKeyEnv     string `yaml:"api_key_env"`
	Proxy         bool   `yaml:"proxy"`
	ProxyOverride string `yaml:"proxy_override"`

	// Resolved
	APIKey string `yaml:"-"`
}

// Load reads and parses the config file, resolving env vars
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Defaults
	if cfg.Gateway.Port == 0 {
		cfg.Gateway.Port = 8080
	}
	if cfg.Gateway.RequestTimeout == 0 {
		cfg.Gateway.RequestTimeout = 90 * time.Second
	}
	if cfg.Gateway.RetryDelay == 0 {
		cfg.Gateway.RetryDelay = 5 * time.Second
	}

	// Env override for request timeout (e.g. GATEWAY_REQUEST_TIMEOUT=90s)
	if v := os.Getenv("GATEWAY_REQUEST_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Gateway.RequestTimeout = d
		}
	}
	if cfg.Telegram.AlertCooldown == 0 {
		cfg.Telegram.AlertCooldown = 5 * time.Minute
	}
	if cfg.Proxy.Type == "" {
		cfg.Proxy.Type = "none"
	}
	if cfg.Proxy.IPMappingsFile == "" {
		cfg.Proxy.IPMappingsFile = "ip_mappings.json"
	}
	if cfg.Aliases == nil {
		cfg.Aliases = make(map[string]string)
	}

	// Resolve env vars
	cfg.Gateway.AuthToken = os.Getenv(cfg.Gateway.AuthTokenEnv)
	cfg.Proxy.APIKey = os.Getenv(cfg.Proxy.APIKeyEnv)
	cfg.Telegram.BotToken = os.Getenv(cfg.Telegram.BotTokenEnv)
	cfg.Telegram.ChatID = os.Getenv(cfg.Telegram.ChatIDEnv)

	// Resolve account API keys, skip accounts with empty keys; default LimitMode
	for i := range cfg.Providers {
		if cfg.Providers[i].LimitMode == "" {
			cfg.Providers[i].LimitMode = "per_model"
		}
		var validAccounts []AccountConfig
		for j := range cfg.Providers[i].Accounts {
			acc := &cfg.Providers[i].Accounts[j]
			acc.APIKey = os.Getenv(acc.APIKeyEnv)
			if acc.APIKey != "" {
				validAccounts = append(validAccounts, *acc)
			}
		}
		cfg.Providers[i].Accounts = validAccounts
	}

	// Validate
	if cfg.Gateway.AuthToken == "" {
		return nil, fmt.Errorf("auth token env var %s is not set", cfg.Gateway.AuthTokenEnv)
	}

	totalAccounts := 0
	for _, p := range cfg.Providers {
		totalAccounts += len(p.Accounts)
	}
	if totalAccounts == 0 {
		return nil, fmt.Errorf("no provider accounts configured (all API key env vars are empty)")
	}

	return &cfg, nil
}

// ResolveAlias resolves a model alias to its canonical name.
// Returns the original name if no alias exists.
func (c *Config) ResolveAlias(model string) string {
	if canonical, ok := c.Aliases[model]; ok {
		return canonical
	}
	return model
}

// FindModelProvider returns the provider config and model config for a given model ID.
func (c *Config) FindModelProvider(modelID string) (*ProviderConfig, *ModelConfig) {
	for i := range c.Providers {
		for j := range c.Providers[i].Models {
			if c.Providers[i].Models[j].ID == modelID {
				return &c.Providers[i], &c.Providers[i].Models[j]
			}
		}
	}
	return nil, nil
}

// ListModels returns all available model IDs
func (c *Config) ListModels() []string {
	var models []string
	for _, p := range c.Providers {
		if len(p.Accounts) == 0 {
			continue
		}
		for _, m := range p.Models {
			models = append(models, m.ID)
		}
	}
	return models
}

// NeedsProxy returns true if any account has proxy enabled
func (c *Config) NeedsProxy() bool {
	for _, p := range c.Providers {
		for _, a := range p.Accounts {
			if a.Proxy && a.ProxyOverride == "" {
				return true
			}
		}
	}
	return false
}

// GetProviderModels returns a human-readable summary for logging
func (c *Config) Summary() string {
	var parts []string
	for _, p := range c.Providers {
		if len(p.Accounts) == 0 {
			continue
		}
		modelNames := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			modelNames = append(modelNames, m.ID)
		}
		parts = append(parts, fmt.Sprintf("%s (%d accounts, models: %s)",
			p.Name, len(p.Accounts), strings.Join(modelNames, ", ")))
	}
	return strings.Join(parts, "; ")
}
