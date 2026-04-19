package provider

import (
	"ai-gateway/logger"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	if req.ResponseFormat != nil {
		body["response_format"] = req.ResponseFormat
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := account.BaseURL + "/chat/completions"

	// B3/I2: Reuse the pre-built HTTP client from the account
	client := account.HTTPClient
	if client == nil {
		logger.Warn().Str("account", account.DisplayName()).Msg("No HTTPClient on account, using default")
		client = http.DefaultClient
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

	// Sync remaining counters from Groq/OpenAI-compatible rate limit headers.
	// These are present on every response and let us avoid sending requests
	// we know will be rejected.
	syncRateLimitHeaders(account, resp.Header)

	if resp.StatusCode == 429 {
		return nil, &RateLimitError{
			StatusCode: 429,
			Body:       string(respBody),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode == 503 {
		// Model-level outage — skip remaining accounts
		return nil, &ModelUnavailableError{StatusCode: 503, Body: string(respBody)}
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("server error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		// Expired / revoked / invalid key
		return nil, &InvalidKeyError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
			Account:    account.DisplayName(),
		}
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

// syncRateLimitHeaders reads Groq's x-ratelimit-remaining-* headers and
// records a short cooldown if either remaining counter is at or near zero.
// Header docs: https://console.groq.com/docs/rate-limits
//
//	x-ratelimit-remaining-requests → RPD remaining
//	x-ratelimit-remaining-tokens   → TPM remaining
func syncRateLimitHeaders(account *Account, h http.Header) {
	remainingReqs := parseIntHeader(h, "x-ratelimit-remaining-requests")
	remainingToks := parseIntHeader(h, "x-ratelimit-remaining-tokens")

	if remainingReqs == 0 {
		// Daily request budget exhausted — cooldown until midnight UTC
		now := time.Now().UTC()
		tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		account.Usage.SetCooldown(time.Until(tomorrow))
		logger.Warn().Str("account", account.DisplayName()).Msg("Daily request budget exhausted (x-ratelimit-remaining-requests=0)")
	} else if remainingToks == 0 {
		// TPM exhausted — short cooldown (reset is typically within the minute)
		account.Usage.SetCooldown(15 * time.Second)
		logger.Debug().Str("account", account.DisplayName()).Msg("TPM budget exhausted (x-ratelimit-remaining-tokens=0)")
	}
}

func parseIntHeader(h http.Header, key string) int {
	val := h.Get(key)
	if val == "" {
		return -1 // not present
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return -1
	}
	return n
}
