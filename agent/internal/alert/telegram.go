package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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

func (t *Telegram) Send(ctx context.Context, chatID, text string) error {
	form := url.Values{
		"chat_id":                  {chatID},
		"text":                     {text},
		"disable_web_page_preview": {"true"},
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
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || !body.OK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, body.Description)
	}
	return nil
}
