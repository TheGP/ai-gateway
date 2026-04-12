package alerts

import (
	"ai-gateway/logger"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// TelegramAlerter sends alerts via Telegram Bot API
type TelegramAlerter struct {
	botToken      string
	chatID        string
	cooldown      time.Duration
	lastAlertTime map[string]time.Time // alertType → lastSent
	mu            sync.Mutex
	enabled       bool
}

func NewTelegramAlerter(botToken, chatID string, cooldown time.Duration) *TelegramAlerter {
	enabled := botToken != "" && chatID != ""
	if !enabled {
		logger.Info().Msg("Telegram alerts disabled (no bot token or chat ID)")
	} else {
		logger.Info().Str("chat_id", chatID).Msg("Telegram alerts enabled")
	}
	return &TelegramAlerter{
		botToken:      botToken,
		chatID:        chatID,
		cooldown:      cooldown,
		lastAlertTime: make(map[string]time.Time),
		enabled:       enabled,
	}
}

// Alert sends a message if cooldown has elapsed for this alert type
func (t *TelegramAlerter) Alert(alertType, message string) {
	if !t.enabled {
		return
	}

	t.mu.Lock()
	if last, ok := t.lastAlertTime[alertType]; ok {
		if time.Since(last) < t.cooldown {
			t.mu.Unlock()
			return
		}
	}
	t.lastAlertTime[alertType] = time.Now()
	t.mu.Unlock()

	go t.send(message)
}

func (t *TelegramAlerter) send(message string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	body := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       message,
		"parse_mode": "HTML",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal telegram message")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		logger.Error().Err(err).Msg("Failed to send telegram alert")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		logger.Warn().Int("status", resp.StatusCode).Msg("Telegram API returned non-200")
	}
}

// AlertExhausted sends a critical alert when all providers are exhausted
func (t *TelegramAlerter) AlertExhausted(model string, accountsSummary string) {
	msg := fmt.Sprintf("🔴 <b>AI Gateway Alert</b>\n\nAll providers exhausted for model %q\n\n%s", model, accountsSummary)
	t.Alert("exhausted_"+model, msg)
}

// AlertCapacity sends a warning when capacity is high
func (t *TelegramAlerter) AlertCapacity(account string, details string) {
	msg := fmt.Sprintf("🟡 <b>AI Gateway Warning</b>\n\nCapacity &gt;80%% on %s\n%s", account, details)
	t.Alert("capacity_"+account, msg)
}

// Alert429Streak sends a warning on consecutive 429s
func (t *TelegramAlerter) Alert429Streak(account string, count int) {
	msg := fmt.Sprintf("🟡 <b>AI Gateway Warning</b>\n\n%s got %d consecutive 429 errors", account, count)
	t.Alert("429_"+account, msg)
}

// AlertInvalidKey sends a critical alert when an API key is found to be expired or invalid.
// It bypasses the standard cooldown so the alert always fires (key won't recover on its own).
func (t *TelegramAlerter) AlertInvalidKey(account string) {
	if !t.enabled {
		return
	}
	msg := fmt.Sprintf("🔑 <b>AI Gateway — Dead API Key</b>\n\nAccount <code>%s</code> returned an expired/invalid key error.\nIt has been <b>disabled</b> until the gateway restarts.", account)
	// Always send — use a unique key so cooldown doesn't suppress it
	go t.send(msg)
}
