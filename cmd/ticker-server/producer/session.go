package producer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
)

// CredentialsSessionProvider adapts the local credentials repository to the
// ticker's refreshable session contract.
type CredentialsSessionProvider struct {
	repository credentials.CredentialsRepository

	mu          sync.Mutex
	initialized bool
}

// NewCredentialsSessionProvider creates a local, persistent session provider.
func NewCredentialsSessionProvider(repository credentials.CredentialsRepository) (*CredentialsSessionProvider, error) {
	if repository == nil {
		return nil, errors.New("ticker producer: credentials repository is required")
	}
	return &CredentialsSessionProvider{repository: repository}, nil
}

// LoadSession reloads credentials from SQLite on every poll. The first load
// validates the stored session; a forced load always performs a fresh login.
func (p *CredentialsSessionProvider) LoadSession(ctx context.Context, userID string, force bool) (Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	stored, err := p.repository.GetUserByID(ctx, userID)
	if err != nil {
		return Session{}, fmt.Errorf("load ticker credentials: %w", err)
	}
	user, err := credentials.NewUser(*stored)
	if err != nil {
		return Session{}, err
	}

	didLogin := false
	if force {
		if err := user.Login(); err != nil {
			return Session{}, fmt.Errorf("refresh ticker session: %w", err)
		}
		user.LastLogin = time.Now()
		didLogin = true
	} else if !p.initialized {
		profile, refreshed, err := user.LoginIfRequired()
		if err != nil {
			return Session{}, fmt.Errorf("validate ticker session: %w", err)
		}
		if profile != nil {
			user.UserName = profile.UserName
			user.Email = profile.Email
			user.Broker = profile.Broker
		}
		if refreshed {
			user.LastLogin = time.Now()
			didLogin = true
		}
	}

	if didLogin {
		if err := p.persist(ctx, user.Credentials); err != nil {
			return Session{}, err
		}
	}
	p.initialized = true
	return sessionFromCredentials(user.Credentials), nil
}

func (p *CredentialsSessionProvider) persist(ctx context.Context, value credentials.Credentials) error {
	update := credentials.CredentialsUpdate{
		UserID:           value.UserID,
		UserName:         &value.UserName,
		Email:            &value.Email,
		Broker:           &value.Broker,
		EncToken:         &value.EncToken,
		RequestToken:     &value.RequestToken,
		AccessToken:      &value.AccessToken,
		KFSessionToken:   &value.KFSessionToken,
		PublicToken:      &value.PublicToken,
		IsAPIUser:        &value.IsAPIUser,
		DataSubscription: &value.DataSubscription,
		LastLogin:        &value.LastLogin,
	}
	if err := p.repository.UpdateUser(ctx, update); err != nil {
		return fmt.Errorf("persist ticker session: %w", err)
	}
	return nil
}

func sessionFromCredentials(value credentials.Credentials) Session {
	return Session{
		UserID:      value.UserID,
		APIKey:      value.APIKey,
		AccessToken: value.AccessToken,
		EncToken:    value.EncToken,
		IsAPIUser:   value.IsAPIUser,
	}
}
