package provider

import (
	"ai-gateway/config"
	"testing"
	"time"
)

func TestCanAcceptPerAccountUsesAccountLimitsWhenSet(t *testing.T) {
	u := NewAccountUsage("midnight_utc")

	u.mu.Lock()
	u.dailyRequests = 1000
	u.dailyResetTime = time.Now().Add(time.Hour)
	u.mu.Unlock()

	ok := u.CanAccept(
		1,
		config.ModelLimits{},
		config.ModelLimits{RPD: 1000},
		"openai/gpt-oss-120b:free",
		"per_account",
	)
	if ok {
		t.Fatal("expected account_limits to block per_account request")
	}
}

func TestCanAcceptPerAccountFallsBackToModelLimitsWithoutAccountLimits(t *testing.T) {
	u := NewAccountUsage("midnight_utc")

	u.mu.Lock()
	u.dailyTokens = 100
	u.dailyResetTime = time.Now().Add(time.Hour)
	u.mu.Unlock()

	ok := u.CanAccept(
		1,
		config.ModelLimits{TPD: 100},
		config.ModelLimits{},
		"llama-3.3-70b-versatile",
		"per_account",
	)
	if ok {
		t.Fatal("expected model limits to remain the fallback for per_account without account_limits")
	}
}

func TestCanAcceptPerModelTracksUsagePerModel(t *testing.T) {
	u := NewAccountUsage("midnight_utc")
	u.RecordRequest(60, 40, "model-a", "per_model")

	if u.CanAccept(1, config.ModelLimits{TPD: 100}, config.ModelLimits{}, "model-a", "per_model") {
		t.Fatal("expected model-a to be blocked by its own TPD usage")
	}

	if !u.CanAccept(1, config.ModelLimits{TPD: 100}, config.ModelLimits{}, "model-b", "per_model") {
		t.Fatal("expected model-b to remain available with separate per-model counters")
	}
}

func TestCanAcceptBothAppliesAccountAndModelLimits(t *testing.T) {
	t.Run("account limit blocks request", func(t *testing.T) {
		u := NewAccountUsage("midnight_utc")

		u.mu.Lock()
		u.dailyRequests = 1
		u.dailyResetTime = time.Now().Add(time.Hour)
		u.mu.Unlock()

		ok := u.CanAccept(
			1,
			config.ModelLimits{TPD: 1000},
			config.ModelLimits{RPD: 1},
			"mistral-large-latest",
			"both",
		)
		if ok {
			t.Fatal("expected account_limits to block both-mode request")
		}
	})

	t.Run("model limit blocks request", func(t *testing.T) {
		u := NewAccountUsage("midnight_utc")
		u.RecordRequest(50, 50, "mistral-large-latest", "both")

		ok := u.CanAccept(
			1,
			config.ModelLimits{TPD: 100},
			config.ModelLimits{RPD: 10},
			"mistral-large-latest",
			"both",
		)
		if ok {
			t.Fatal("expected per-model limits to block both-mode request")
		}
	})
}
