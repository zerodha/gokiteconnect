package credentials

import (
	"context"
	"time"
)

type CredentialsRepository interface {
	GetUserByID(ctx context.Context, userID string) (*Credentials, error)
	CreateUser(ctx context.Context, credentials Credentials) error
	UpdateUser(ctx context.Context, update CredentialsUpdate) error
	DeleteUser(ctx context.Context, userID string) error
	ListAvailableUsers(ctx context.Context) ([]Credentials, error)
}

// CredentialsUpdate contains a partial update for a user identified by UserID.
type CredentialsUpdate struct {
	UserID string

	UserName       *string
	Email          *string
	Broker         *string
	TelegramChatID *string

	Password   *string
	TOTPSecret *string
	APIKey     *string
	APISecret  *string

	EncToken       *string
	RequestToken   *string
	AccessToken    *string
	KFSessionToken *string
	PublicToken    *string

	IsAPIUser        *bool
	DataSubscription *bool
	LastLogin        *time.Time
}

type loginResponse struct {
	Status string `json:"status"`
	Data   struct {
		UserID      string   `json:"user_id"`
		RequestID   string   `json:"request_id"`
		TwoFAType   string   `json:"twofa_type"`
		TwoFATypes  []string `json:"twofa_types"`
		TwoFAStatus string   `json:"twofa_status"`
		Profile     struct {
			UserName      string `json:"user_name"`
			UserShortName string `json:"user_shortname"`
			AvatarURL     string `json:"avatar_url"`
		} `json:"profile"`
		TwoFAValue string `json:"twofa_value"`
	} `json:"data"`
}

type twofaResponse struct {
	Data struct {
		AttemptsRemaining int `json:"attempts_remaining"`
	}
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
	Status    string `json:"status"`
}
