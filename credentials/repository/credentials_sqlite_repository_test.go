package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	credentialspkg "github.com/devshoe/gokiteconnect/credentials"
	_ "github.com/mattn/go-sqlite3"
)

func openSQLiteRepository(t *testing.T) (*sql.DB, *UserSQLiteRepository) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	repository, err := NewUserSQLiteRepository(db)
	if err != nil {
		t.Fatalf("NewUserSQLiteRepository returned an error: %v", err)
	}
	return db, repository
}

func completeTestCredentials(userID string) credentialspkg.Credentials {
	return credentialspkg.Credentials{
		UserID:           userID,
		UserName:         "Test User",
		Email:            "user@example.com",
		Broker:           "ZERODHA",
		TelegramChatID:   "12345",
		Password:         "password",
		TOTPSecret:       "secret",
		APIKey:           "api-key",
		APISecret:        "api-secret",
		EncToken:         "enc-token",
		RequestToken:     "request-token",
		AccessToken:      "access-token",
		KFSessionToken:   "kf-session",
		PublicToken:      "public-token",
		IsAPIUser:        true,
		DataSubscription: true,
		CreatedAt:        time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt:        time.Date(2025, time.February, 3, 4, 5, 6, 0, time.UTC),
		LastLogin:        time.Date(2025, time.March, 4, 5, 6, 7, 0, time.UTC),
	}
}

func TestNewUserSQLiteRepositoryRequiresDatabase(t *testing.T) {
	if _, err := NewUserSQLiteRepository(nil); err == nil {
		t.Fatal("expected a nil database error")
	}
}

func TestUserSQLiteRepositoryRoundTripsCredentials(t *testing.T) {
	_, repository := openSQLiteRepository(t)
	ctx := context.Background()
	want := completeTestCredentials("AB1234")

	if err := repository.CreateUser(ctx, want); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	got, err := repository.GetUserByID(ctx, want.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}

	assertCredentialsEqual(t, *got, want)
}

func TestUserSQLiteRepositoryUsesTimestampDefaults(t *testing.T) {
	_, repository := openSQLiteRepository(t)
	credentials := completeTestCredentials("DEFAULTS")
	credentials.CreatedAt = time.Time{}
	credentials.UpdatedAt = time.Time{}
	credentials.LastLogin = time.Time{}

	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	got, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("database timestamp defaults were not applied")
	}
	if !got.LastLogin.IsZero() {
		t.Fatal("zero LastLogin should round-trip as unset")
	}
}

func TestUserSQLiteRepositoryAppliesPartialUpdate(t *testing.T) {
	_, repository := openSQLiteRepository(t)
	ctx := context.Background()
	original := completeTestCredentials("UPDATE")
	original.UpdatedAt = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := repository.CreateUser(ctx, original); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}

	email := "updated@example.com"
	emptyAPIKey := ""
	zeroLogin := time.Time{}
	if err := repository.UpdateUser(ctx, credentialspkg.CredentialsUpdate{
		UserID:    original.UserID,
		Email:     &email,
		APIKey:    &emptyAPIKey,
		LastLogin: &zeroLogin,
	}); err != nil {
		t.Fatalf("UpdateUser returned an error: %v", err)
	}
	got, err := repository.GetUserByID(ctx, original.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if got.Email != email {
		t.Fatalf("unexpected email: %q", got.Email)
	}
	if got.APIKey != "" {
		t.Fatalf("expected API key to be cleared, got %q", got.APIKey)
	}
	if got.UserName != original.UserName || got.Password != original.Password {
		t.Fatal("partial update changed untouched fields")
	}
	if !got.LastLogin.IsZero() {
		t.Fatal("zero LastLogin should clear the stored timestamp")
	}
	if !got.UpdatedAt.After(original.UpdatedAt) {
		t.Fatalf("updated_at was not advanced: %v", got.UpdatedAt)
	}
}

func TestUserSQLiteRepositoryListDeleteAndChatID(t *testing.T) {
	_, repository := openSQLiteRepository(t)
	ctx := context.Background()
	for _, userID := range []string{"B", "A"} {
		credentials := completeTestCredentials(userID)
		credentials.TelegramChatID = ""
		if err := repository.CreateUser(ctx, credentials); err != nil {
			t.Fatalf("CreateUser(%s) returned an error: %v", userID, err)
		}
	}

	users, err := repository.ListAvailableUsers(ctx)
	if err != nil {
		t.Fatalf("ListAvailableUsers returned an error: %v", err)
	}
	if len(users) != 2 || users[0].UserID != "A" || users[1].UserID != "B" {
		t.Fatalf("unexpected users: %#v", users)
	}
	if err := repository.SetChatID(ctx, "A", "chat-id"); err != nil {
		t.Fatalf("SetChatID returned an error: %v", err)
	}
	chatID, err := repository.GetChatID(ctx, "A")
	if err != nil {
		t.Fatalf("GetChatID returned an error: %v", err)
	}
	if chatID != "chat-id" {
		t.Fatalf("unexpected chat ID: %q", chatID)
	}

	if err := repository.DeleteUser(ctx, "A"); err != nil {
		t.Fatalf("DeleteUser returned an error: %v", err)
	}
	if _, err := repository.GetUserByID(ctx, "A"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows after delete, got %v", err)
	}
}

func TestUserSQLiteRepositoryMigratesTradebotSchema(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	const oldSchema = `
	CREATE TABLE users (
		record_id INTEGER PRIMARY KEY AUTOINCREMENT,
		full_name TEXT NOT NULL,
		email TEXT NOT NULL,
		broker TEXT NOT NULL,
		api_user BOOLEAN NOT NULL DEFAULT false,
		data_subscription BOOLEAN NOT NULL DEFAULT false,
		telegram_chat_id TEXT DEFAULT NULL,
		user_id TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		totp_secret TEXT NOT NULL,
		api_key TEXT,
		api_secret TEXT,
		enc_token TEXT NOT NULL,
		access_token TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_login TIMESTAMP DEFAULT NULL
	);`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	repository, err := NewUserSQLiteRepository(db)
	if err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	for _, column := range []string{"request_token", "kf_session", "public_token"} {
		exists, err := repository.hasUserColumn(column)
		if err != nil {
			t.Fatalf("inspect %s column: %v", column, err)
		}
		if !exists {
			t.Fatalf("migration did not add %s", column)
		}
	}

	credentials := completeTestCredentials("MIGRATED")
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser after migration returned an error: %v", err)
	}
	got, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID after migration returned an error: %v", err)
	}
	if got.RequestToken != credentials.RequestToken || got.KFSessionToken != credentials.KFSessionToken {
		t.Fatal("migrated token columns did not round-trip")
	}
}

func assertCredentialsEqual(t *testing.T, got, want credentialspkg.Credentials) {
	t.Helper()
	if got.UserID != want.UserID || got.UserName != want.UserName || got.Email != want.Email || got.Broker != want.Broker {
		t.Fatalf("profile fields differ: got %#v want %#v", got, want)
	}
	if got.TelegramChatID != want.TelegramChatID || got.Password != want.Password || got.TOTPSecret != want.TOTPSecret {
		t.Fatalf("web credentials differ: got %#v want %#v", got, want)
	}
	if got.APIKey != want.APIKey || got.APISecret != want.APISecret || got.IsAPIUser != want.IsAPIUser {
		t.Fatalf("API credentials differ: got %#v want %#v", got, want)
	}
	if got.EncToken != want.EncToken || got.RequestToken != want.RequestToken || got.AccessToken != want.AccessToken ||
		got.KFSessionToken != want.KFSessionToken || got.PublicToken != want.PublicToken {
		t.Fatalf("session tokens differ: got %#v want %#v", got, want)
	}
	if got.DataSubscription != want.DataSubscription || !got.CreatedAt.Equal(want.CreatedAt) ||
		!got.UpdatedAt.Equal(want.UpdatedAt) || !got.LastLogin.Equal(want.LastLogin) {
		t.Fatalf("metadata differs: got %#v want %#v", got, want)
	}
}
