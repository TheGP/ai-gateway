package provider

import (
	"ai-gateway/logger"
	"ai-gateway/proxy"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAISend sends a ChatRequest to an OpenAI-compatible API
// (Groq, Mistral, Cerebras, or any provider with the same format).
func OpenAISend(ctx context.Context, account *Account, req ChatRequest) (*ChatResponse, error) {
	// OpenAI format — pass through directly
	body := map[string]interface{}{
		"model":    req.Model,
		"messages": req.Messages,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := account.BaseURL + "/chat/completions"

	// Create HTTP client with optional proxy
	client := &http.Client{Timeout: 60 * time.Second}
	if account.ProxyInfo != nil {
		proxiedClient, err := proxy.MakeHTTPClient(account.ProxyInfo, 60)
		if err != nil {
			logger.Warn().Err(err).Str("account", account.DisplayName()).Msg("Failed to create proxied client, using direct")
		} else {
			client = proxiedClient
			client.Timeout = 60 * time.Second
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+account.APIKey)

	start := time.Now()
	resp, err := client.Do(httpReq)
	duration := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	logger.Debug().Str("account", account.DisplayName()).Str("model", req.Model).Dur("duration", duration).Int("status", resp.StatusCode).Msg("OpenAI API response")

	if resp.StatusCode == 429 {
		return nil, &RateLimitError{
			StatusCode: 429,
			Body:       string(respBody),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from provider")
	}

	return &chatResp, nil
}
