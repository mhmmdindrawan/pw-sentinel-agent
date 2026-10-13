package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Destination is one Telegram chat, optionally one forum topic inside it.
type Destination struct {
	ChatID string
	// ThreadID is the message_thread_id of a forum topic; 0 posts to the
	// chat itself (the General topic in a forum supergroup).
	ThreadID int
}

// RateLimitError is Telegram's HTTP 429: nothing more may be sent to this
// chat until RetryAfter has passed.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("telegram: rate limited, retry after %s", e.RetryAfter)
}

// Telegram sends messages through the Bot API. Messages are plain text (no
// parse_mode) so text coming from kiosks can't inject formatting or links.
type Telegram struct {
	Token   string
	BaseURL string // default https://api.telegram.org; overridable for tests
	Client  *http.Client
}

func NewTelegram(token string) *Telegram {
	return &Telegram{Token: token, BaseURL: "https://api.telegram.org", Client: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Telegram) Send(ctx context.Context, to Destination, text string) error {
	form := url.Values{
		"chat_id":                  {to.ChatID},
		"text":                     {text},
		"disable_web_page_preview": {"true"},
	}
	if to.ThreadID > 0 {
		form.Set("message_thread_id", strconv.Itoa(to.ThreadID))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.BaseURL+"/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.Client.Do(req)
	if err != nil {
		// The URL embeds the bot token; never let it reach the logs.
		return fmt.Errorf("telegram request failed: %s", strings.ReplaceAll(err.Error(), t.Token, "<token>"))
	}
	defer resp.Body.Close()
	var body struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode == http.StatusTooManyRequests {
		wait := time.Duration(body.Parameters.RetryAfter) * time.Second
		if wait <= 0 {
			wait = 5 * time.Second
		}
		return &RateLimitError{RetryAfter: wait}
	}
	if body.Parameters.MigrateToChatID != 0 {
		// Turning on Topics (or other supergroup features) gives a group a
		// new id; the old one stops working for good.
		return fmt.Errorf("telegram: chat %s became a supergroup, set chat_id to %d", to.ChatID, body.Parameters.MigrateToChatID)
	}
	if resp.StatusCode != http.StatusOK || !body.OK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, body.Description)
	}
	return nil
}
