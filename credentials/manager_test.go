package credentials

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const validTOTPSecret = "JBSWY3DPEHPK3PXP"

type fakeAuthenticationBackend struct {
	mu               sync.Mutex
	webLoginRequests int
	profileRequests  int
}

func (backend *fakeAuthenticationBackend) newClient(credentials Credentials) (*User, error) {
	user, err := NewUser(credentials)
	if err != nil {
		return nil, err
	}
	user.httpClient.Transport = roundTripFunc(backend.roundTripLogin)
	user.kiteHTTPClient.Transport = roundTripFunc(backend.roundTripProfile)
	return user, nil
}

func (backend *fakeAuthenticationBackend) roundTripLogin(req *http.Request) (*http.Response, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()

	header := make(http.Header)
	body := `{"status":"success","data":{}}`
	if strings.HasSuffix(req.URL.Path, "/api/login") {
		backend.webLoginRequests++
		body = `{"status":"success","data":{"request_id":"request-id"}}`
		header.Add("Set-Cookie", "kf_session=fresh-kf; Path=/")
	}
	if strings.HasSuffix(req.URL.Path, "/api/twofa") {
		header.Add("Set-Cookie", "enctoken=fresh-enc; Path=/")
		header.Add("Set-Cookie", "public_token=fresh-public; Path=/")
	}
	return httpTestResponse(req, http.StatusOK, header, body), nil
}

func (backend *fakeAuthenticationBackend) roundTripProfile(req *http.Request) (*http.Response, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.profileRequests++

	authorization := req.Header.Get("Authorization")
	if authorization != "enctoken valid-enc" && authorization != "enctoken fresh-enc" {
		return httpTestResponse(req, http.StatusForbidden, nil,
			`{"status":"error","error_type":"TokenException","message":"expired"}`), nil
	}
	return httpTestResponse(req, http.StatusOK, nil,
		`{"status":"success","data":{"user_id":"AB1234","user_name":"Updated User","email":"updated@example.com","broker":"ZERODHA"}}`), nil
}

func (backend *fakeAuthenticationBackend) loginCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.webLoginRequests
}

func httpTestResponse(req *http.Request, status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       &trackingBody{Reader: strings.NewReader(body)},
		Request:    req,
	}
}

type countingCredentialsRepository struct {
	CredentialsRepository
	mu       sync.Mutex
	getCalls int
}

type memoryCredentialsRepository struct {
	mu    sync.Mutex
	users map[string]Credentials
}

func newMemoryCredentialsRepository() *memoryCredentialsRepository {
	return &memoryCredentialsRepository{users: make(map[string]Credentials)}
}

func (repository *memoryCredentialsRepository) GetUserByID(_ context.Context, userID string) (*Credentials, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	credentials, ok := repository.users[userID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &credentials, nil
}

func (repository *memoryCredentialsRepository) CreateUser(_ context.Context, credentials Credentials) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	if _, ok := repository.users[credentials.UserID]; ok {
		return errors.New("user already exists")
	}
	now := time.Now()
	if credentials.CreatedAt.IsZero() {
		credentials.CreatedAt = now
	}
	if credentials.UpdatedAt.IsZero() {
		credentials.UpdatedAt = now
	}
	repository.users[credentials.UserID] = credentials
	return nil
}

func (repository *memoryCredentialsRepository) UpdateUser(_ context.Context, update CredentialsUpdate) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	credentials, ok := repository.users[update.UserID]
	if !ok {
		return sql.ErrNoRows
	}
	update.apply(&credentials)
	credentials.UpdatedAt = time.Now()
	repository.users[update.UserID] = credentials
	return nil
}

func (repository *memoryCredentialsRepository) DeleteUser(_ context.Context, userID string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	delete(repository.users, userID)
	return nil
}

func (repository *memoryCredentialsRepository) ListAvailableUsers(_ context.Context) ([]Credentials, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	users := make([]Credentials, 0, len(repository.users))
	for _, credentials := range repository.users {
		users = append(users, credentials)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	return users, nil
}

var _ CredentialsRepository = (*memoryCredentialsRepository)(nil)

func (repository *countingCredentialsRepository) GetUserByID(ctx context.Context, userID string) (*Credentials, error) {
	repository.mu.Lock()
	repository.getCalls++
	repository.mu.Unlock()
	return repository.CredentialsRepository.GetUserByID(ctx, userID)
}

func (repository *countingCredentialsRepository) getCount() int {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return repository.getCalls
}

func managerTestCredentials() Credentials {
	return Credentials{
		UserID:     "AB1234",
		Password:   "password",
		TOTPSecret: validTOTPSecret,
	}
}

func newTestManager(t *testing.T, repository CredentialsRepository, backend *fakeAuthenticationBackend) *UserManager {
	t.Helper()
	manager, err := NewUserManager(repository)
	if err != nil {
		t.Fatalf("NewUserManager returned an error: %v", err)
	}
	manager.newUserClient = backend.newClient
	return manager
}

func TestNewUserManagerRequiresRepository(t *testing.T) {
	if _, err := NewUserManager(nil); err == nil {
		t.Fatal("expected a nil repository error")
	}
}

func TestUserManagerCachesLoadedUser(t *testing.T) {
	sqliteRepository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "valid-enc"
	if err := sqliteRepository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	countingRepository := &countingCredentialsRepository{CredentialsRepository: sqliteRepository}
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, countingRepository, backend)

	first, err := manager.GetUser(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("first GetUser returned an error: %v", err)
	}
	second, err := manager.GetUser(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("second GetUser returned an error: %v", err)
	}
	if first != second {
		t.Fatal("GetUser did not return the cached user")
	}
	if countingRepository.getCount() != 1 {
		t.Fatalf("expected one repository read, got %d", countingRepository.getCount())
	}
	if backend.loginCount() != 0 {
		t.Fatal("valid cached credentials triggered a login")
	}
}

func TestUserManagerRefreshesExpiredCredentialsOnGet(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "expired-enc"
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, repository, backend)

	user, err := manager.GetUser(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUser returned an error: %v", err)
	}
	if backend.loginCount() != 1 {
		t.Fatalf("expected one login, got %d", backend.loginCount())
	}
	if user.EncToken != "fresh-enc" || user.KFSessionToken != "fresh-kf" || user.PublicToken != "fresh-public" {
		t.Fatalf("new session was not captured: %#v", user.Credentials)
	}
	if user.UserName != "Updated User" || user.Email != "updated@example.com" || user.Broker != "ZERODHA" {
		t.Fatalf("profile was not applied: %#v", user.Credentials)
	}
	if user.LastLogin.IsZero() {
		t.Fatal("LastLogin was not set")
	}
	stored, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if stored.EncToken != user.EncToken || stored.UserName != user.UserName || stored.LastLogin.IsZero() {
		t.Fatalf("refreshed credentials were not persisted: %#v", stored)
	}
}

func TestUserManagerCreatesAuthenticatedUser(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, repository, backend)
	credentials := managerTestCredentials()

	if err := manager.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	stored, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if stored.EncToken != "fresh-enc" || stored.UserName != "Updated User" || stored.LastLogin.IsZero() {
		t.Fatalf("created credentials were not authenticated: %#v", stored)
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatal("manager did not initialize timestamps")
	}
	if _, ok := manager.currentlyLoadedUsers[credentials.UserID]; !ok {
		t.Fatal("created user was not cached")
	}
}

func TestUserManagerUpdateAvoidsLoginForNonAuthenticationFields(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "valid-enc"
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, repository, backend)
	email := "new@example.com"

	if err := manager.UpdateUser(context.Background(), CredentialsUpdate{UserID: credentials.UserID, Email: &email}); err != nil {
		t.Fatalf("UpdateUser returned an error: %v", err)
	}
	if backend.loginCount() != 0 {
		t.Fatal("non-authentication update triggered a login")
	}
	if manager.currentlyLoadedUsers[credentials.UserID].Email != email {
		t.Fatal("updated user was not cached")
	}
}

func TestUserManagerAuthenticationUpdateClearsAndRefreshesTokens(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "valid-enc"
	credentials.AccessToken = "stale-access"
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, repository, backend)
	newPassword := "new-password"

	if err := manager.UpdateUser(context.Background(), CredentialsUpdate{
		UserID:   credentials.UserID,
		Password: &newPassword,
	}); err != nil {
		t.Fatalf("UpdateUser returned an error: %v", err)
	}
	stored, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if backend.loginCount() != 1 || stored.Password != newPassword || stored.EncToken != "fresh-enc" {
		t.Fatalf("authentication update was not refreshed: %#v", stored)
	}
	if stored.AccessToken != "" {
		t.Fatalf("stale API access token was retained: %q", stored.AccessToken)
	}
}

func TestUserManagerForcesRefreshAndPersistsSession(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "valid-enc"
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	backend := &fakeAuthenticationBackend{}
	manager := newTestManager(t, repository, backend)

	user, err := manager.RefreshUserSession(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("RefreshUserSession returned an error: %v", err)
	}
	if backend.loginCount() != 1 || user.EncToken != "fresh-enc" {
		t.Fatalf("session was not refreshed: %#v", user.Credentials)
	}
	stored, err := repository.GetUserByID(context.Background(), credentials.UserID)
	if err != nil {
		t.Fatalf("GetUserByID returned an error: %v", err)
	}
	if stored.EncToken != "fresh-enc" || stored.LastLogin.IsZero() {
		t.Fatal("forced refresh was not persisted")
	}
}

func TestUserManagerListChatAndDeleteSynchronizeRepositoryAndCache(t *testing.T) {
	repository := newMemoryCredentialsRepository()
	credentials := managerTestCredentials()
	credentials.EncToken = "valid-enc"
	if err := repository.CreateUser(context.Background(), credentials); err != nil {
		t.Fatalf("CreateUser returned an error: %v", err)
	}
	manager := newTestManager(t, repository, &fakeAuthenticationBackend{})
	if _, err := manager.GetUser(context.Background(), credentials.UserID); err != nil {
		t.Fatalf("GetUser returned an error: %v", err)
	}

	if err := manager.SetChatID(context.Background(), credentials.UserID, "chat-id"); err != nil {
		t.Fatalf("SetChatID returned an error: %v", err)
	}
	chatID, err := manager.GetChatID(context.Background(), credentials.UserID)
	if err != nil || chatID != "chat-id" {
		t.Fatalf("unexpected chat ID %q, error %v", chatID, err)
	}
	users, err := manager.ListUsers(context.Background())
	if err != nil || len(users) != 1 {
		t.Fatalf("unexpected users %#v, error %v", users, err)
	}
	if err := manager.DeleteUser(context.Background(), credentials.UserID); err != nil {
		t.Fatalf("DeleteUser returned an error: %v", err)
	}
	if _, ok := manager.currentlyLoadedUsers[credentials.UserID]; ok {
		t.Fatal("deleted user remained cached")
	}
	if _, err := repository.GetUserByID(context.Background(), credentials.UserID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted row to be absent, got %v", err)
	}
}
