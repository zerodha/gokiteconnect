package credentials

import (
	"errors"
	"time"
)

// Credentials stores the credentials and session tokens for a Zerodha user.
type Credentials struct {
	UserID         string
	UserName       string
	Email          string
	Broker         string
	TelegramChatID string

	Password   string
	TOTPSecret string
	APIKey     string
	APISecret  string

	EncToken       string
	RequestToken   string
	AccessToken    string
	KFSessionToken string
	PublicToken    string

	IsAPIUser        bool
	DataSubscription bool

	CreatedAt time.Time
	UpdatedAt time.Time
	LastLogin time.Time
}

func (credentials Credentials) validate() error {
	if credentials.UserID == "" {
		return errors.New("credentials.validate: user ID is required")
	}
	if credentials.Password == "" {
		return errors.New("credentials.validate: password is required")
	}
	if credentials.TOTPSecret == "" {
		return errors.New("credentials.validate: TOTP secret is required")
	}
	if credentials.IsAPIUser {
		if credentials.APIKey == "" {
			return errors.New("credentials.validate: API key is required for an API user")
		}
		if credentials.APISecret == "" {
			return errors.New("credentials.validate: API secret is required for an API user")
		}
	}
	return nil
}

func (credentials *Credentials) clearTokens() {
	credentials.EncToken = ""
	credentials.RequestToken = ""
	credentials.AccessToken = ""
	credentials.KFSessionToken = ""
	credentials.PublicToken = ""
}

func (update CredentialsUpdate) apply(credentials *Credentials) {
	if update.UserName != nil {
		credentials.UserName = *update.UserName
	}
	if update.Email != nil {
		credentials.Email = *update.Email
	}
	if update.Broker != nil {
		credentials.Broker = *update.Broker
	}
	if update.TelegramChatID != nil {
		credentials.TelegramChatID = *update.TelegramChatID
	}
	if update.Password != nil {
		credentials.Password = *update.Password
	}
	if update.TOTPSecret != nil {
		credentials.TOTPSecret = *update.TOTPSecret
	}
	if update.APIKey != nil {
		credentials.APIKey = *update.APIKey
	}
	if update.APISecret != nil {
		credentials.APISecret = *update.APISecret
	}
	if update.EncToken != nil {
		credentials.EncToken = *update.EncToken
	}
	if update.RequestToken != nil {
		credentials.RequestToken = *update.RequestToken
	}
	if update.AccessToken != nil {
		credentials.AccessToken = *update.AccessToken
	}
	if update.KFSessionToken != nil {
		credentials.KFSessionToken = *update.KFSessionToken
	}
	if update.PublicToken != nil {
		credentials.PublicToken = *update.PublicToken
	}
	if update.IsAPIUser != nil {
		credentials.IsAPIUser = *update.IsAPIUser
	}
	if update.DataSubscription != nil {
		credentials.DataSubscription = *update.DataSubscription
	}
	if update.LastLogin != nil {
		credentials.LastLogin = *update.LastLogin
	}
}

func (update CredentialsUpdate) authenticationChanged() bool {
	return update.Password != nil ||
		update.TOTPSecret != nil ||
		update.APIKey != nil ||
		update.APISecret != nil ||
		update.IsAPIUser != nil
}
