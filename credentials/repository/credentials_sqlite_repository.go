package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	credentialspkg "github.com/devshoe/gokiteconnect/credentials"
)

// UserSQLiteRepository stores user credentials in SQLite.
type UserSQLiteRepository struct {
	db *sql.DB
}

// NewUserSQLiteRepository creates a SQLite repository and migrates its schema.
func NewUserSQLiteRepository(db *sql.DB) (*UserSQLiteRepository, error) {
	if db == nil {
		return nil, errors.New("credentials.NewUserSQLiteRepository: database is required")
	}
	repository := &UserSQLiteRepository{db: db}
	if err := repository.migrate(); err != nil {
		return nil, err
	}
	return repository, nil
}

// CreateUser inserts a credentials row.
func (repository *UserSQLiteRepository) CreateUser(ctx context.Context, credentials credentialspkg.Credentials) error {
	const query = `
	INSERT INTO users (
		full_name,
		email,
		broker,
		api_user,
		data_subscription,
		telegram_chat_id,
		user_id,
		password,
		totp_secret,
		api_key,
		api_secret,
		enc_token,
		request_token,
		access_token,
		kf_session,
		public_token,
		created_at,
		updated_at,
		last_login
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		COALESCE(?, CURRENT_TIMESTAMP),
		COALESCE(?, CURRENT_TIMESTAMP),
		?)
	`
	_, err := repository.db.ExecContext(ctx, query,
		credentials.UserName,
		credentials.Email,
		credentials.Broker,
		credentials.IsAPIUser,
		credentials.DataSubscription,
		emptyToNil(credentials.TelegramChatID),
		credentials.UserID,
		credentials.Password,
		credentials.TOTPSecret,
		// API credentials are optional for web users. Use an empty string rather
		// than NULL so this remains compatible with older users tables that
		// declared these columns NOT NULL.
		credentials.APIKey,
		credentials.APISecret,
		credentials.EncToken,
		credentials.RequestToken,
		credentials.AccessToken,
		credentials.KFSessionToken,
		credentials.PublicToken,
		nullableTimeArg(credentials.CreatedAt),
		nullableTimeArg(credentials.UpdatedAt),
		nullableTimeArg(credentials.LastLogin),
	)
	return err
}

// GetUserByID returns one credentials row by its Zerodha user ID.
func (repository *UserSQLiteRepository) GetUserByID(ctx context.Context, userID string) (*credentialspkg.Credentials, error) {
	row := repository.db.QueryRowContext(ctx, selectCredentialsQuery+" WHERE user_id = ?", userID)
	return repository.scanCredentials(row)
}

// UpdateUser applies a partial credentials update.
func (repository *UserSQLiteRepository) UpdateUser(ctx context.Context, update credentialspkg.CredentialsUpdate) error {
	if update.UserID == "" {
		return errors.New("credentials.UpdateUser: user ID is required")
	}

	setClauses := make([]string, 0, 17)
	values := make([]interface{}, 0, 18)
	appendStringUpdate := func(column string, value *string) {
		if value != nil {
			setClauses = append(setClauses, column+" = ?")
			values = append(values, *value)
		}
	}

	appendStringUpdate("full_name", update.UserName)
	appendStringUpdate("email", update.Email)
	appendStringUpdate("broker", update.Broker)
	if update.TelegramChatID != nil {
		setClauses = append(setClauses, "telegram_chat_id = ?")
		values = append(values, emptyToNil(*update.TelegramChatID))
	}
	appendStringUpdate("password", update.Password)
	appendStringUpdate("totp_secret", update.TOTPSecret)
	if update.APIKey != nil {
		setClauses = append(setClauses, "api_key = ?")
		values = append(values, *update.APIKey)
	}
	if update.APISecret != nil {
		setClauses = append(setClauses, "api_secret = ?")
		values = append(values, *update.APISecret)
	}
	appendStringUpdate("enc_token", update.EncToken)
	appendStringUpdate("request_token", update.RequestToken)
	appendStringUpdate("access_token", update.AccessToken)
	appendStringUpdate("kf_session", update.KFSessionToken)
	appendStringUpdate("public_token", update.PublicToken)
	if update.IsAPIUser != nil {
		setClauses = append(setClauses, "api_user = ?")
		values = append(values, *update.IsAPIUser)
	}
	if update.DataSubscription != nil {
		setClauses = append(setClauses, "data_subscription = ?")
		values = append(values, *update.DataSubscription)
	}
	if update.LastLogin != nil {
		setClauses = append(setClauses, "last_login = ?")
		values = append(values, nullableTimeArg(*update.LastLogin))
	}
	if len(setClauses) == 0 {
		return nil
	}

	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")
	query := "UPDATE users SET " + strings.Join(setClauses, ", ") + " WHERE user_id = ?"
	values = append(values, update.UserID)
	_, err := repository.db.ExecContext(ctx, query, values...)
	return err
}

// DeleteUser deletes a credentials row by user ID.
func (repository *UserSQLiteRepository) DeleteUser(ctx context.Context, userID string) error {
	_, err := repository.db.ExecContext(ctx, "DELETE FROM users WHERE user_id = ?", userID)
	return err
}

// ListAvailableUsers returns all stored credentials ordered by user ID.
func (repository *UserSQLiteRepository) ListAvailableUsers(ctx context.Context) ([]credentialspkg.Credentials, error) {
	rows, err := repository.db.QueryContext(ctx, selectCredentialsQuery+" ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	credentials := make([]credentialspkg.Credentials, 0)
	for rows.Next() {
		userCredentials, err := repository.scanCredentials(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, *userCredentials)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return credentials, nil
}

// SetChatID stores the Telegram chat ID for a user.
func (repository *UserSQLiteRepository) SetChatID(ctx context.Context, userID, chatID string) error {
	_, err := repository.db.ExecContext(ctx,
		"UPDATE users SET telegram_chat_id = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?",
		emptyToNil(chatID), userID,
	)
	return err
}

// GetChatID returns the Telegram chat ID for a user.
func (repository *UserSQLiteRepository) GetChatID(ctx context.Context, userID string) (string, error) {
	row := repository.db.QueryRowContext(ctx, "SELECT telegram_chat_id FROM users WHERE user_id = ?", userID)
	var chatID sql.NullString
	if err := row.Scan(&chatID); err != nil {
		return "", err
	}
	return chatID.String, nil
}

const selectCredentialsQuery = `
	SELECT
		full_name,
		email,
		broker,
		api_user,
		data_subscription,
		COALESCE(telegram_chat_id, ''),
		user_id,
		password,
		totp_secret,
		COALESCE(api_key, ''),
		COALESCE(api_secret, ''),
		enc_token,
		request_token,
		access_token,
		kf_session,
		public_token,
		created_at,
		updated_at,
		last_login
	FROM users`

func (repository *UserSQLiteRepository) scanCredentials(row scanner) (*credentialspkg.Credentials, error) {
	var (
		credentials credentialspkg.Credentials
		lastLogin   sql.NullTime
	)
	if err := row.Scan(
		&credentials.UserName,
		&credentials.Email,
		&credentials.Broker,
		&credentials.IsAPIUser,
		&credentials.DataSubscription,
		&credentials.TelegramChatID,
		&credentials.UserID,
		&credentials.Password,
		&credentials.TOTPSecret,
		&credentials.APIKey,
		&credentials.APISecret,
		&credentials.EncToken,
		&credentials.RequestToken,
		&credentials.AccessToken,
		&credentials.KFSessionToken,
		&credentials.PublicToken,
		&credentials.CreatedAt,
		&credentials.UpdatedAt,
		&lastLogin,
	); err != nil {
		return nil, err
	}
	if lastLogin.Valid {
		credentials.LastLogin = lastLogin.Time
	}
	return &credentials, nil
}

func (repository *UserSQLiteRepository) migrate() error {
	const createUsersTable = `
	CREATE TABLE IF NOT EXISTS users (
		record_id INTEGER PRIMARY KEY AUTOINCREMENT,
		full_name TEXT NOT NULL DEFAULT '',
		email TEXT NOT NULL DEFAULT '',
		broker TEXT NOT NULL DEFAULT '',
		api_user BOOLEAN NOT NULL DEFAULT false,
		data_subscription BOOLEAN NOT NULL DEFAULT false,
		telegram_chat_id TEXT DEFAULT NULL,
		user_id TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		totp_secret TEXT NOT NULL,
		api_key TEXT DEFAULT NULL,
		api_secret TEXT DEFAULT NULL,
		enc_token TEXT NOT NULL DEFAULT '',
		request_token TEXT NOT NULL DEFAULT '',
		access_token TEXT NOT NULL DEFAULT '',
		kf_session TEXT NOT NULL DEFAULT '',
		public_token TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_login TIMESTAMP DEFAULT NULL
	);`
	if _, err := repository.db.Exec(createUsersTable); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "request_token", definition: "request_token TEXT NOT NULL DEFAULT ''"},
		{name: "kf_session", definition: "kf_session TEXT NOT NULL DEFAULT ''"},
		{name: "public_token", definition: "public_token TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		exists, err := repository.hasUserColumn(column.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := repository.db.Exec("ALTER TABLE users ADD COLUMN " + column.definition); err != nil {
				return err
			}
		}
	}
	return nil
}

func (repository *UserSQLiteRepository) hasUserColumn(name string) (bool, error) {
	rows, err := repository.db.Query("PRAGMA table_info(users)")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid          int
			columnName   string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if columnName == name {
			return true, nil
		}
	}
	return false, rows.Err()
}

func nullableTimeArg(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value
}

func emptyToNil(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

type scanner interface {
	Scan(dest ...interface{}) error
}

var _ credentialspkg.CredentialsRepository = (*UserSQLiteRepository)(nil)
