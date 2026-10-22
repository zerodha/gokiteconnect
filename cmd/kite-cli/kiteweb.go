package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devshoe/gokiteconnect/credentials"
	"github.com/devshoe/gokiteconnect/models"
	"github.com/devshoe/gokiteconnect/pkg/kiteweb"
)

// webOpener opens Kite web signed in as user.
type webOpener func(ctx context.Context, user credentials.Credentials) error

// kiteWebOpener opens Kite web in Chrome with user's stored web session, in
// the profile dir/<USER>. It checks that Kite web accepts the session first,
// and never logs in.
func kiteWebOpener(dir string) webOpener {
	return func(ctx context.Context, user credentials.Credentials) error {
		userID, err := models.NormalizeUserID(user.UserID)
		if err != nil {
			return err
		}
		session := kiteweb.Session{
			UserID: userID, EncToken: user.EncToken, KFSession: user.KFSessionToken, PublicToken: user.PublicToken,
		}
		if err := checkWebSession(ctx, session); err != nil {
			return err
		}
		return kiteweb.Open(ctx, session, kiteweb.Options{ProfileDir: filepath.Join(dir, userID)})
	}
}

// checkWebSession asks Kite web whether it accepts the stored session, so
// that a refused session fails here and not on Kite web's login page.
func checkWebSession(ctx context.Context, session kiteweb.Session) error {
	if strings.TrimSpace(session.EncToken) == "" {
		return errors.New("no web session stored · refresh it with F")
	}
	err := kiteweb.Check(ctx, &http.Client{Timeout: 15 * time.Second}, session)
	if errors.Is(err, kiteweb.ErrSessionRefused) || errors.Is(err, kiteweb.ErrIncompleteSession) {
		return fmt.Errorf("Kite web no longer accepts %s's stored web session (its kf_session has expired) · refresh it with F", session.UserID)
	}
	return err
}

// openKiteWeb opens Kite web as the selected user.
func (a *app) openKiteWeb() tea.Cmd {
	user, ok := a.users.selected()
	if !ok {
		a.showError(errNoUser)
		return nil
	}
	if a.deps.openWeb == nil {
		a.notify(statusWarn("Kite web is not available"))
		return nil
	}
	return a.userOperation("Opening Kite web…",
		fmt.Sprintf("Kite web opened for %s · logging out there ends this stored session", user.UserID),
		user.UserID, func(ctx context.Context) error {
			return a.deps.openWeb(ctx, user)
		})
}
