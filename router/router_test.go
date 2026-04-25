package router

import (
	"ai-gateway/config"
	"ai-gateway/provider"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRouteTriesSameModelAcrossProvidersBeforeTierFallback(t *testing.T) {
	shared := config.ModelConfig{ID: "openai/gpt-oss-120b", Tier: 3}
	cfg := testConfig(
		testProviderConfig("groq", "openai", []config.ModelConfig{shared}),
		testProviderConfig("openrouter-paid", "openai", []config.ModelConfig{{ID: "openai/gpt-oss-120b", Tier: 99}}),
	)

	groq := testAccount("groq", "GROQ_API_KEY_1", "openai", "per_model", shared)
	paid := testAccount("openrouter-paid", "OPENROUTER_API_KEY", "openai", "per_model", config.ModelConfig{ID: "openai/gpt-oss-120b", Tier: 99})
	groq.SetLastUsed(time.Now().Add(-2 * time.Hour))
	paid.SetLastUsed(time.Now().Add(-1 * time.Hour))

	r := New([]*provider.Account{groq, paid}, cfg, nil)

	var attempts []string
	r.send = func(ctx context.Context, account *provider.Account, req provider.ChatRequest) (*provider.ChatResponse, error) {
		attempts = append(attempts, account.DisplayName()+":"+req.Model)
		if account.ProviderName == "groq" {
			return nil, errors.New("groq exhausted")
		}
		return testResponse(req.Model), nil
	}

	resp, err := r.Route(context.Background(), provider.ChatRequest{
		Model:    "openai/gpt-oss-120b",
		Messages: []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if resp.XGateway == nil || resp.XGateway.Provider != "openrouter-paid" {
		t.Fatalf("expected openrouter-paid response metadata, got %+v", resp.XGateway)
	}

	wantAttempts := []string{
		"groq/GROQ_API_KEY_1:openai/gpt-oss-120b",
		"openrouter-paid/OPENROUTER_API_KEY:openai/gpt-oss-120b",
	}
	if !reflect.DeepEqual(attempts, wantAttempts) {
		t.Fatalf("unexpected attempt order: got %v want %v", attempts, wantAttempts)
	}
}

func TestRouteWithProviderRestrictionSkipsOtherProviders(t *testing.T) {
	shared := config.ModelConfig{ID: "openai/gpt-oss-120b", Tier: 3}
	cfg := testConfig(
		testProviderConfig("groq", "openai", []config.ModelConfig{shared}),
		testProviderConfig("openrouter-paid", "openai", []config.ModelConfig{{ID: "openai/gpt-oss-120b", Tier: 99}}),
	)

	groq := testAccount("groq", "GROQ_API_KEY_1", "openai", "per_model", shared)
	paid := testAccount("openrouter-paid", "OPENROUTER_API_KEY", "openai", "per_model", config.ModelConfig{ID: "openai/gpt-oss-120b", Tier: 99})
	groq.SetLastUsed(time.Now().Add(-2 * time.Hour))
	paid.SetLastUsed(time.Now().Add(-1 * time.Hour))

	r := New([]*provider.Account{groq, paid}, cfg, nil)

	var attempts []string
	r.send = func(ctx context.Context, account *provider.Account, req provider.ChatRequest) (*provider.ChatResponse, error) {
		attempts = append(attempts, account.DisplayName()+":"+req.Model)
		return nil, errors.New("forced provider failed")
	}

	_, err := r.Route(context.Background(), provider.ChatRequest{
		Model:     "openai/gpt-oss-120b",
		XProvider: "groq",
		Messages:  []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected Route to fail when the forced provider fails")
	}

	wantAttempts := []string{
		"groq/GROQ_API_KEY_1:openai/gpt-oss-120b",
		"groq/GROQ_API_KEY_1:openai/gpt-oss-120b",
	}
	if !reflect.DeepEqual(attempts, wantAttempts) {
		t.Fatalf("unexpected attempts with x_provider: got %v want %v", attempts, wantAttempts)
	}
}

func TestRouteUsesExplicitFallbackBeforeAutomaticTierFallback(t *testing.T) {
	primary := config.ModelConfig{ID: "mistral-large-latest", Tier: 3}
	explicit := config.ModelConfig{ID: "llama-3.3-70b-versatile", Tier: 3}
	auto := config.ModelConfig{ID: "openai/gpt-oss-120b", Tier: 99}

	cfg := testConfig(
		testProviderConfig("mistral", "openai", []config.ModelConfig{primary}),
		testProviderConfig("groq", "openai", []config.ModelConfig{explicit}),
		testProviderConfig("openrouter-paid", "openai", []config.ModelConfig{auto}),
	)

	primaryAccount := testAccount("mistral", "MISTRAL_API_KEY_1", "openai", "both", primary)
	explicitAccount := testAccount("groq", "GROQ_API_KEY_1", "openai", "per_model", explicit)
	autoAccount := testAccount("openrouter-paid", "OPENROUTER_API_KEY", "openai", "per_model", auto)
	primaryAccount.SetLastUsed(time.Now().Add(-3 * time.Hour))
	explicitAccount.SetLastUsed(time.Now().Add(-2 * time.Hour))
	autoAccount.SetLastUsed(time.Now().Add(-1 * time.Hour))

	r := New([]*provider.Account{primaryAccount, explicitAccount, autoAccount}, cfg, nil)

	var attempts []string
	r.send = func(ctx context.Context, account *provider.Account, req provider.ChatRequest) (*provider.ChatResponse, error) {
		attempts = append(attempts, account.DisplayName()+":"+req.Model)
		if req.Model == "mistral-large-latest" {
			return nil, errors.New("primary failed")
		}
		if req.Model == "llama-3.3-70b-versatile" {
			return testResponse(req.Model), nil
		}
		return nil, errors.New("automatic fallback should not be reached")
	}

	resp, err := r.Route(context.Background(), provider.ChatRequest{
		Model:           "mistral-large-latest",
		XFallbackModels: []string{"llama-3.3-70b-versatile"},
		Messages:        []provider.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if resp.Model != "llama-3.3-70b-versatile" {
		t.Fatalf("expected explicit fallback model, got %q", resp.Model)
	}

	wantAttempts := []string{
		"mistral/MISTRAL_API_KEY_1:mistral-large-latest",
		"groq/GROQ_API_KEY_1:llama-3.3-70b-versatile",
	}
	if !reflect.DeepEqual(attempts, wantAttempts) {
		t.Fatalf("unexpected attempts: got %v want %v", attempts, wantAttempts)
	}
}

func testConfig(providers ...config.ProviderConfig) *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{
			RequestTimeout: time.Second,
			RetryDelay:     0,
		},
		Providers: providers,
	}
}

func testProviderConfig(name, providerType string, models []config.ModelConfig) config.ProviderConfig {
	return config.ProviderConfig{
		Name:   name,
		Type:   providerType,
		Models: models,
		Accounts: []config.AccountConfig{
			{APIKeyEnv: name + "_KEY"},
		},
	}
}

func testAccount(providerName, apiKeyEnv, providerType, limitMode string, models ...config.ModelConfig) *provider.Account {
	return &provider.Account{
		ProviderName: providerName,
		ProviderType: providerType,
		APIKeyEnv:    apiKeyEnv,
		Models:       models,
		LimitMode:    limitMode,
		Usage:        provider.NewAccountUsage("midnight_utc"),
	}
}

func testResponse(model string) *provider.ChatResponse {
	return &provider.ChatResponse{
		Model: model,
		Choices: []provider.Choice{
			{
				Index:        0,
				Message:      provider.Message{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			},
		},
		Usage: provider.Usage{TotalTokens: 1},
	}
}
