package portfolio

import (
	"context"
	"fmt"
	"time"

	"github.com/devshoe/gokiteconnect/credentials"
)

const kiteRequestTimeout = 30 * time.Second

// StoredSessions opens Kite clients with each user's stored session. It
// never logs in, since a fresh login could end a session another tool is
// using: a user whose session has expired fails until a tool that may log
// in, such as kite-cli, refreshes it.
func StoredSessions(store credentials.CredentialsRepository) Sources {
	return func(ctx context.Context, userID string) (Source, error) {
		creds, err := store.GetUserByID(ctx, userID)
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", userID, err)
		}
		user, err := credentials.NewUser(*creds)
		if err != nil {
			return nil, fmt.Errorf("user %s: %w", userID, err)
		}
		client := user.KiteClient()
		client.SetAppName("portfolio-service")
		client.SetTimeout(kiteRequestTimeout)
		return client, nil
	}
}
