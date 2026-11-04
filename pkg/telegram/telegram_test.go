package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "123456:test-token"

func TestSendPostsTheMessage(t *testing.T) {
	var path, chat, text string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = r.ParseForm()
		chat, text = r.PostForm.Get("chat_id"), r.PostForm.Get("text")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	bot, err := New(testToken, server.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.Send(context.Background(), " 42 ", "NIFTY above 25,100"); err != nil {
		t.Fatal(err)
	}
	if path != "/bot"+testToken+"/sendMessage" || chat != "42" || text != "NIFTY above 25,100" {
		t.Fatalf("sent %s chat %q text %q", path, chat, text)
	}
}

func TestSendErrorsLeaveOutTheToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	bot, _ := New(testToken, server.URL)
	err := bot.Send(context.Background(), "42", "hello")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("Send() error = %v, want Telegram's description", err)
	}
	server.Close()

	// A closed server fails in the transport, whose error names the URL.
	err = bot.Send(context.Background(), "42", "hello")
	if err == nil {
		t.Fatal("Send() to a closed server succeeded")
	}
	if strings.Contains(err.Error(), "test-token") {
		t.Fatalf("error %q shows the token", err)
	}
}

func TestBotsNeedATokenAndChats(t *testing.T) {
	if _, err := New("  ", ""); err == nil {
		t.Fatal("New() without a token succeeded")
	}
	bot, _ := New(testToken, "")
	if bot.baseURL != DefaultBaseURL {
		t.Fatalf("base URL = %q", bot.baseURL)
	}
	if err := bot.Send(context.Background(), " ", "hello"); err == nil {
		t.Fatal("Send() without a chat succeeded")
	}
}
