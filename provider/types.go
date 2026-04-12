package provider

import (
	"ai-gateway/config"
	"ai-gateway/proxy"
	"fmt"
	"sync"
	"time"
)

// Account wraps a single API key with its proxy, usage tracking, and state
type Account struct {
	ProviderName  string
	ProviderType  string // "gemini" or "openai"
	BaseURL       string
	APIKey        string
	APIKeyEnv     string // for display (mask the actual key)
	Models        []config.ModelConfig
	UseProxy      bool
	ProxyOverride string
	DailyReset    string
	LimitMode     string             // "per_model" | "per_account" | "both"
	AccountLimits config.ModelLimits // shared limits used in "both" mode

	ProxyInfo *proxy.ProxyInfo
	Usage     *AccountUsage
	Disabled  bool // set true when the API key is permanently invalid
	LastUsed  time.Time
	mu        sync.Mutex
}

// SupportsModel checks if this account can serve the given model
func (a *Account) SupportsModel(modelID string) bool {
	for _, m := range a.Models {
		if m.ID == modelID {
			return true
		}
	}
	return false
}

// GetModelLimits returns the limits for a specific model on this account
func (a *Account) GetModelLimits(modelID string) config.ModelLimits {
	for _, m := range a.Models {
		if m.ID == modelID {
			return m.Limits
		}
	}
	return config.ModelLimits{}
}

// GetModelTier returns the tier for a specific model
func (a *Account) GetModelTier(modelID string) int {
	for _, m := range a.Models {
		if m.ID == modelID {
			return m.Tier
		}
	}
	return 0
}

// DisplayName returns a short identifier for logging/dashboard
func (a *Account) DisplayName() string {
	return a.ProviderName + "/" + a.APIKeyEnv
}

// RateLimitError is returned on 429 responses
type RateLimitError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit %d: %s", e.StatusCode, truncate(e.Body, 200))
}

// ModelUnavailableError is returned on 503 "high demand" / UNAVAILABLE responses.
// This is a model-level outage — retrying other accounts for the same model is
// pointless; the router should skip straight to a fallback model.
type ModelUnavailableError struct {
	StatusCode int
	Body       string
}

func (e *ModelUnavailableError) Error() string {
	return fmt.Sprintf("model unavailable %d: %s", e.StatusCode, truncate(e.Body, 200))
}

// InvalidKeyError is returned when the API key is expired or permanently invalid
// (e.g. Gemini 400 INVALID_ARGUMENT with "API key expired").
// The router will disable the account and send a Telegram alert.
type InvalidKeyError struct {
	StatusCode int
	Body       string
	Account    string // display name, set by the provider send function
}

func (e *InvalidKeyError) Error() string {
	return fmt.Sprintf("invalid/expired API key on %s: %s", e.Account, truncate(e.Body, 200))
}

// Message is an OpenAI-compatible chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the OpenAI-compatible request format
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	XProvider   string    `json:"x_provider,omitempty"`
}

// ChatResponse is the OpenAI-compatible response format
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`

	// Gateway metadata
	XGateway *GatewayMetadata `json:"x_gateway,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type GatewayMetadata struct {
	OriginalModel string `json:"original_model,omitempty"`
	Provider      string `json:"provider"`
	Account       string `json:"account"`
	Fallback      bool   `json:"fallback,omitempty"`
}

// truncate shortens a string to maxLen characters for safe log/error embedding.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
