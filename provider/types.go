package provider

import (
	"ai-gateway/config"
	"ai-gateway/proxy"
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

	ProxyInfo *proxy.ProxyInfo
	Usage     *AccountUsage
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
	Upgraded      bool   `json:"upgraded,omitempty"`
}
