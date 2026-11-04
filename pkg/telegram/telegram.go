// Package telegram sends messages through a Telegram bot.
package telegram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Telegram Bot API.
const DefaultBaseURL = "https://api.telegram.org"

const sendTimeout = 15 * time.Second

// Bot sends messages as the bot whose token it holds. Its errors never
// include the token, which the request URL carries.
type Bot struct {
	token   string
	baseURL string
	client  *http.Client
}

// New is the bot with token, sending to the Bot API at baseURL; an empty
// baseURL is DefaultBaseURL.
func New(token, baseURL string) (*Bot, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("telegram: a bot token is required")
	}
	return &Bot{
		token:   token,
		baseURL: strings.TrimRight(cmp.Or(strings.TrimSpace(baseURL), DefaultBaseURL), "/"),
		client:  &http.Client{Timeout: sendTimeout},
	}, nil
}

// Send sends text to the chat chatID, a numeric chat ID or an @channel.
func (b *Bot) Send(ctx context.Context, chatID, text string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return errors.New("telegram: a chat ID is required")
	}
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/bot"+b.token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: build request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := b.client.Do(request)
	if err != nil {
		// A *url.Error names the URL, and so the token: keep only its cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("telegram: send: %w", err)
	}
	defer response.Body.Close()
	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_ = json.Unmarshal(body, &reply)
	if response.StatusCode != http.StatusOK || !reply.OK {
		return fmt.Errorf("telegram: send: %s", cmp.Or(reply.Description, response.Status))
	}
	return nil
}
