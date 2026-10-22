package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	credentialstore "github.com/devshoe/gokiteconnect/credentials/repository"
	"github.com/devshoe/gokiteconnect/kite"
	"github.com/devshoe/gokiteconnect/models"
	instrumentstore "github.com/devshoe/gokiteconnect/repository"
)

// fundamentalsIndexTag is the index the summary counts on its own.
const fundamentalsIndexTag = "Nifty 500"

// fundamentalsSummary describes the stored fundamentals.
type fundamentalsSummary struct {
	count       int
	indexCount  int
	refreshedAt time.Time
}

// fundamentalsStore keeps instrument fundamentals read from Kite's
// screener. Refresh reports each page it reads to progress.
type fundamentalsStore interface {
	Summary(context.Context) (fundamentalsSummary, error)
	Refresh(ctx context.Context, progress func(page, pages int)) (fundamentalsSummary, error)
}

// localFundamentals stores fundamentals in the SQLite database at path,
// reading them with the data user's stored web session. It never logs in.
type localFundamentals struct {
	path            string
	credentialsPath string
	dataUserID      string
	// screenerURI replaces Kite's screener API root, for tests.
	screenerURI string
}

func (f localFundamentals) open(ctx context.Context) (*instrumentstore.FundamentalsSQLiteRepository, error) {
	return instrumentstore.NewFundamentalsSQLiteRepository(ctx, f.path)
}

func (f localFundamentals) Summary(ctx context.Context) (fundamentalsSummary, error) {
	repo, err := f.open(ctx)
	if err != nil {
		return fundamentalsSummary{}, err
	}
	defer repo.Close()
	return summariseFundamentals(ctx, repo)
}

func summariseFundamentals(ctx context.Context, repo *instrumentstore.FundamentalsSQLiteRepository) (fundamentalsSummary, error) {
	var summary fundamentalsSummary
	var err error
	if summary.count, err = repo.Count(ctx); err != nil {
		return summary, err
	}
	index, err := repo.List(ctx, fundamentalsIndexTag)
	if err != nil {
		return summary, err
	}
	summary.indexCount = len(index)
	summary.refreshedAt, err = repo.LastRefreshedAt(ctx)
	return summary, err
}

// Refresh reads every instrument the screener has, by market cap, and
// replaces the stored fundamentals with them once all pages are read.
func (f localFundamentals) Refresh(ctx context.Context, progress func(page, pages int)) (fundamentalsSummary, error) {
	screener, err := f.screener(ctx)
	if err != nil {
		return fundamentalsSummary{}, err
	}
	request := kite.ScreenerRequest{Query: "last_price>=0", OrderBy: "market_cap", Order: "desc", Page: 1, PerPage: kite.ScreenerMaxPerPage}
	var read []models.Fundamentals
	for {
		page, err := screener.GetInstruments(ctx, request)
		if errors.Is(err, kite.ErrScreenerSession) {
			return fundamentalsSummary{}, fmt.Errorf("%w · refresh the data user's session on the users screen (F)", err)
		}
		if err != nil {
			return fundamentalsSummary{}, err
		}
		if progress != nil {
			progress(request.Page, page.TotalPages)
		}
		for _, result := range page.Results {
			fundamentals, err := result.Fundamentals()
			if err != nil {
				return fundamentalsSummary{}, err
			}
			read = append(read, fundamentals)
		}
		if len(page.Results) == 0 || request.Page >= page.TotalPages {
			break
		}
		request.Page++
	}

	repo, err := f.open(ctx)
	if err != nil {
		return fundamentalsSummary{}, err
	}
	defer repo.Close()
	if err := repo.Replace(ctx, read, time.Now()); err != nil {
		return fundamentalsSummary{}, err
	}
	return summariseFundamentals(ctx, repo)
}

// screener signs in to the screener as the data user.
func (f localFundamentals) screener(ctx context.Context) (*kite.ScreenerClient, error) {
	if _, err := os.Stat(f.credentialsPath); err != nil {
		return nil, fmt.Errorf("credentials database %s: %w", f.credentialsPath, err)
	}
	database, err := sql.Open("sqlite3", f.credentialsPath)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	store, err := credentialstore.NewUserSQLiteRepository(database)
	if err != nil {
		return nil, err
	}
	user, err := dataUser(ctx, store, f.dataUserID)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc([]string{user.EncToken, user.KFSessionToken, user.PublicToken}, func(value string) bool { return strings.TrimSpace(value) == "" }) {
		return nil, fmt.Errorf("data user %s has no web session · refresh it on the users screen (F)", user.UserID)
	}
	screener := kite.NewScreenerClient(user.UserID, user.EncToken, user.KFSessionToken, user.PublicToken)
	if f.screenerURI != "" {
		screener.SetBaseURI(f.screenerURI)
	}
	return screener, nil
}

// fundamentalsMsg reports the stored fundamentals, after a refresh when
// refreshed is set.
type fundamentalsMsg struct {
	summary   fundamentalsSummary
	err       error
	refreshed bool
}

// loadFundamentals reads the stored fundamentals' summary, refreshing them
// from Kite's screener first when refresh is set.
func (a *app) loadFundamentals(refresh bool) tea.Cmd {
	store := a.deps.fundamentals
	if store == nil {
		if refresh {
			a.admin.summaryStatus = statusWarn("Fundamentals are unavailable.")
		}
		return nil
	}
	if !refresh {
		return a.task(func(ctx context.Context) tea.Msg {
			summary, err := store.Summary(ctx)
			return fundamentalsMsg{summary: summary, err: err}
		})
	}
	return tea.Batch(a.busy("Reading fundamentals from Kite's screener…"), a.task(func(ctx context.Context) tea.Msg {
		summary, err := store.Refresh(ctx, func(page, pages int) {
			a.progress(fmt.Sprintf("Reading fundamentals from Kite's screener (page %d/%d)…", page, pages))
		})
		return fundamentalsMsg{summary: summary, err: err, refreshed: true}
	}))
}

func (a *app) handleFundamentals(msg fundamentalsMsg) {
	ad := a.admin
	if msg.refreshed {
		a.closeOverlay()
	}
	if msg.err != nil {
		ad.fundamentalsErr = msg.err.Error()
		if msg.refreshed {
			ad.summaryStatus = statusErr("Fundamentals refresh failed:", msg.err.Error())
		}
		return
	}
	ad.fundamentals, ad.fundamentalsErr = msg.summary, ""
	if msg.refreshed {
		ad.summaryStatus = statusOK(fmt.Sprintf("Fundamentals refreshed from Kite's screener · %s instruments, %s in the %s",
			formatThousands(msg.summary.count), formatThousands(msg.summary.indexCount), fundamentalsIndexTag))
	}
}

// fundamentalsStale is how old fundamentals get before the summary asks
// for a refresh.
const fundamentalsStale = 31 * 24 * time.Hour

// fundamentalsLine is the summary's Fundamentals line.
func (ad *adminScreen) fundamentalsLine() string {
	f := ad.fundamentals
	switch {
	case ad.fundamentalsErr != "":
		return styleRed.Render(ad.fundamentalsErr)
	case f.refreshedAt.IsZero():
		return styleFaint.Render("never read · press F to read them from Kite's screener")
	}
	line := styleText.Render(formatThousands(f.count)+" instruments") +
		styleFaint.Render(" · ") + styleYellow.Render(formatThousands(f.indexCount)) + styleFaint.Render(" in the "+fundamentalsIndexTag+" · read ") +
		styleText.Render(f.refreshedAt.In(models.ExchangeLocation).Format("02 Jan 2006 15:04"))
	age := time.Since(f.refreshedAt)
	if age > fundamentalsStale {
		return line + styleYellow.Render(" · "+relativeAge(age)+", press F to refresh")
	}
	return line + styleFaint.Render(" · "+relativeAge(age))
}
