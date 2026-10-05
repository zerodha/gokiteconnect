package credentials

import (
	"context"
	"errors"
	"sync"
	"time"

	kiteconnect "github.com/devshoe/gokiteconnect"
)

type currentlyLoadedUsers map[string]*User

type userFactory func(Credentials) (*User, error)

// UserManager validates, caches, and persists Zerodha users.
type UserManager struct {
	mu                    sync.Mutex
	currentlyLoadedUsers  currentlyLoadedUsers
	credentialsRepository CredentialsRepository
	newUserClient         userFactory
}

// NewUserManager creates a login-aware user manager.
func NewUserManager(repository CredentialsRepository) (*UserManager, error) {
	if repository == nil {
		return nil, errors.New("credentials.NewUserManager: repository is required")
	}
	return &UserManager{
		currentlyLoadedUsers:  make(currentlyLoadedUsers),
		credentialsRepository: repository,
		newUserClient:         NewUser,
	}, nil
}

// GetUser returns a cached user or loads and validates it from the repository.
func (manager *UserManager) GetUser(ctx context.Context, userID string) (*User, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if user, ok := manager.currentlyLoadedUsers[userID]; ok {
		return user, nil
	}

	credentials, err := manager.credentialsRepository.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	user, err := manager.newUserClient(*credentials)
	if err != nil {
		return nil, err
	}
	profile, didLogin, err := user.LoginIfRequired()
	if err != nil {
		return nil, err
	}
	applyProfile(user, profile)
	if didLogin {
		user.LastLogin = time.Now()
		if err := manager.persistAuthenticatedUser(ctx, user); err != nil {
			return nil, err
		}
	}
	manager.currentlyLoadedUsers[userID] = user
	return user, nil
}

// CreateUser validates a user's credentials, persists them, and caches the user.
func (manager *UserManager) CreateUser(ctx context.Context, credentials Credentials) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	user, err := manager.newUserClient(credentials)
	if err != nil {
		return err
	}
	profile, didLogin, err := user.LoginIfRequired()
	if err != nil {
		return err
	}
	applyProfile(user, profile)
	now := time.Now()
	if didLogin {
		user.LastLogin = now
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = now
	}
	if err := manager.credentialsRepository.CreateUser(ctx, user.Credentials); err != nil {
		return err
	}
	stored, err := manager.credentialsRepository.GetUserByID(ctx, user.UserID)
	if err != nil {
		return err
	}
	user.Credentials = *stored
	manager.currentlyLoadedUsers[user.UserID] = user
	return nil
}

// UpdateUser applies a partial update and refreshes authentication when needed.
func (manager *UserManager) UpdateUser(ctx context.Context, update CredentialsUpdate) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	credentials, err := manager.credentialsRepository.GetUserByID(ctx, update.UserID)
	if err != nil {
		return err
	}
	update.apply(credentials)
	if update.authenticationChanged() {
		credentials.clearTokens()
	}
	user, err := manager.newUserClient(*credentials)
	if err != nil {
		return err
	}

	if update.authenticationChanged() {
		profile, didLogin, err := user.LoginIfRequired()
		if err != nil {
			return err
		}
		applyProfile(user, profile)
		if didLogin {
			user.LastLogin = time.Now()
		}
		update = completeCredentialsUpdate(user.Credentials)
	}
	if err := manager.credentialsRepository.UpdateUser(ctx, update); err != nil {
		return err
	}

	stored, err := manager.credentialsRepository.GetUserByID(ctx, update.UserID)
	if err != nil {
		return err
	}
	user, err = manager.newUserClient(*stored)
	if err != nil {
		return err
	}
	manager.currentlyLoadedUsers[update.UserID] = user
	return nil
}

// RefreshUserSession forces a new Zerodha login and persists the new session.
func (manager *UserManager) RefreshUserSession(ctx context.Context, userID string) (*User, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	user, ok := manager.currentlyLoadedUsers[userID]
	if !ok {
		credentials, err := manager.credentialsRepository.GetUserByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		user, err = manager.newUserClient(*credentials)
		if err != nil {
			return nil, err
		}
	}
	if err := user.Login(); err != nil {
		return nil, err
	}
	profile, err := user.KiteClient().GetUserProfile()
	if err != nil {
		return nil, err
	}
	applyProfile(user, &profile)
	user.LastLogin = time.Now()
	if err := manager.persistAuthenticatedUser(ctx, user); err != nil {
		return nil, err
	}
	manager.currentlyLoadedUsers[userID] = user
	return user, nil
}

// DeleteUser deletes a user and evicts it from the cache.
func (manager *UserManager) DeleteUser(ctx context.Context, userID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if err := manager.credentialsRepository.DeleteUser(ctx, userID); err != nil {
		return err
	}
	delete(manager.currentlyLoadedUsers, userID)
	return nil
}

// ListUsers returns all persisted credentials without triggering login.
func (manager *UserManager) ListUsers(ctx context.Context) ([]Credentials, error) {
	return manager.credentialsRepository.ListAvailableUsers(ctx)
}

// SetChatID updates a user's Telegram chat ID and any cached copy.
func (manager *UserManager) SetChatID(ctx context.Context, userID, chatID string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if err := manager.credentialsRepository.UpdateUser(ctx, CredentialsUpdate{
		UserID:         userID,
		TelegramChatID: &chatID,
	}); err != nil {
		return err
	}
	if user, ok := manager.currentlyLoadedUsers[userID]; ok {
		user.TelegramChatID = chatID
	}
	return nil
}

// GetChatID returns a user's Telegram chat ID.
func (manager *UserManager) GetChatID(ctx context.Context, userID string) (string, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if user, ok := manager.currentlyLoadedUsers[userID]; ok {
		return user.TelegramChatID, nil
	}
	credentials, err := manager.credentialsRepository.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return credentials.TelegramChatID, nil
}

func (manager *UserManager) persistAuthenticatedUser(ctx context.Context, user *User) error {
	if err := manager.credentialsRepository.UpdateUser(ctx, completeCredentialsUpdate(user.Credentials)); err != nil {
		return err
	}
	stored, err := manager.credentialsRepository.GetUserByID(ctx, user.UserID)
	if err != nil {
		return err
	}
	user.Credentials = *stored
	return nil
}

func applyProfile(user *User, profile *kiteconnect.UserProfile) {
	if profile == nil {
		return
	}
	user.UserName = profile.UserName
	user.Email = profile.Email
	user.Broker = profile.Broker
}

func completeCredentialsUpdate(credentials Credentials) CredentialsUpdate {
	return CredentialsUpdate{
		UserID:           credentials.UserID,
		UserName:         &credentials.UserName,
		Email:            &credentials.Email,
		Broker:           &credentials.Broker,
		TelegramChatID:   &credentials.TelegramChatID,
		Password:         &credentials.Password,
		TOTPSecret:       &credentials.TOTPSecret,
		APIKey:           &credentials.APIKey,
		APISecret:        &credentials.APISecret,
		EncToken:         &credentials.EncToken,
		RequestToken:     &credentials.RequestToken,
		AccessToken:      &credentials.AccessToken,
		KFSessionToken:   &credentials.KFSessionToken,
		PublicToken:      &credentials.PublicToken,
		IsAPIUser:        &credentials.IsAPIUser,
		DataSubscription: &credentials.DataSubscription,
		LastLogin:        &credentials.LastLogin,
	}
}
