package paper

import (
	"math"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

var ist = models.ExchangeLocation

// The equity and equity-derivative session, as offsets from midnight IST:
// orders are taken from the open to the close, and intraday (MIS)
// positions are squared off at squareOffAt.
const (
	sessionOpen = 9*time.Hour + 15*time.Minute
	squareOffAt = 15*time.Hour + 20*time.Minute
	sessionEnd  = 15*time.Hour + 30*time.Minute
)

// marketIndex is quoted with every check: an index ticks all session, so a
// stale one means the exchange is shut, as on a holiday.
const marketIndex = "NSE:NIFTY 50"

// staleIndex is how old the index's last tick gets before the market
// counts as shut.
const staleIndex = 10 * time.Minute

// exchanges are the exchanges paper accounts trade on.
var exchanges = map[string]bool{"NSE": true, "BSE": true, "NFO": true, "BFO": true}

// dayOf is midnight IST of t's trading day.
func dayOf(t time.Time) time.Time {
	t = t.In(ist)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ist)
}

// nextDay is midnight IST of the calendar day after day.
func nextDay(day time.Time) time.Time {
	day = day.In(ist)
	return time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, ist)
}

// isWeekday reports whether day can be a trading day. Exchange holidays
// are found from the market index instead.
func isWeekday(day time.Time) bool {
	switch day.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return true
}

// inSession reports whether t falls between the open and the close of a
// weekday.
func inSession(t time.Time) bool {
	day := dayOf(t)
	return isWeekday(day) && !t.Before(day.Add(sessionOpen)) && t.Before(day.Add(sessionEnd))
}

func floorMinute(t time.Time) time.Time { return t.Truncate(time.Minute) }

// onTick reports whether price is a whole number of ticks.
func onTick(price, tick float64) bool {
	if tick <= 0 {
		return true
	}
	ticks := price / tick
	return math.Abs(ticks-math.Round(ticks)) < 1e-6
}

// roundTick rounds price to a tick, up when up is set and down otherwise.
func roundTick(price, tick float64, up bool) float64 {
	if tick <= 0 {
		return price
	}
	ticks := price / tick
	if up {
		ticks = math.Ceil(ticks - 1e-9)
	} else {
		ticks = math.Floor(ticks + 1e-9)
	}
	return math.Round(ticks*tick*1e4) / 1e4
}
