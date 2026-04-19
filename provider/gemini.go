package provider

import (
	"ai-gateway/logger"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GeminiSend sends a ChatRequest to Google AI Studio's Gemini API,
// translating from OpenAI format to Gemini format and back.
func GeminiSend(ctx context.Context, account *Account, req ChatRequest) (*ChatResponse, error) {
	// Convert OpenAI messages → Gemini contents
	contents := make([]map[string]interface{}, 0, len(req.Messages))
	systemParts := []string{}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, msg.Content)
			continue
		}
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]interface{}{
			"role": role,
			"parts": []map[string]interface{}{
				{"text": msg.Content},
			},
		})
	}

	// Build request body
	genConfig := map[string]interface{}{
		"temperature":     0.7,
		"maxOutputTokens": 8192,
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_object" && !strings.HasPrefix(req.Model, "gemma") {
		genConfig["responseMimeType"] = "application/json"
	}

	body := map[string]interface{}{
		"contents":         contents,
		"generationConfig": genConfig,
	}

	// Add system instruction if present
	if len(systemParts) > 0 {
		body["systemInstruction"] = map[string]interface{}{
			"parts": []map[string]interface{}{
				{"text": strings.Join(systemParts, "\n")},
			},
		}
	}

	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build URL
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", account.BaseURL, req.Model, account.APIKey)

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

	logger.Debug().Str("account", account.DisplayName()).Str("model", req.Model).Dur("duration", duration).Int("status", resp.StatusCode).Msg("Gemini API response")

	if resp.StatusCode == 429 {
		return nil, &RateLimitError{
			StatusCode: 429,
			Body:       string(respBody),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if resp.StatusCode == 503 {
		// Model-level outage ("high demand") — no point retrying other accounts
		return nil, &ModelUnavailableError{StatusCode: 503, Body: string(respBody)}
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("gemini server error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if resp.StatusCode == 400 {
		// Detect permanently-dead API key
		if isExpiredKeyBody(string(respBody)) {
			return nil, &InvalidKeyError{
				StatusCode: 400,
				Body:       string(respBody),
				Account:    account.DisplayName(),
			}
		}
		return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("gemini API error %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	// Parse Gemini response
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err := json.Unmarshal(respBody, &geminiResp); err != nil {
		return nil, fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from Gemini")
	}

	// Convert to OpenAI format
	text := geminiResp.Candidates[0].Content.Parts[0].Text
	finishReason := "stop"
	if geminiResp.Candidates[0].FinishReason != "" {
		fr := strings.ToLower(geminiResp.Candidates[0].FinishReason)
		if fr == "max_tokens" || fr == "length" {
			finishReason = "length"
		}
	}

	return &ChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: "assistant", Content: text},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 60 * time.Second
	}
	// Try parsing as seconds
	var seconds int
	if _, err := fmt.Sscanf(header, "%d", &seconds); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 60 * time.Second
}

// isExpiredKeyBody checks the Gemini error body for known "key is dead" messages.
func isExpiredKeyBody(body string) bool {
	return strings.Contains(body, "API key expired") ||
		strings.Contains(body, "API_KEY_INVALID") ||
		strings.Contains(body, "API key not valid")
}
