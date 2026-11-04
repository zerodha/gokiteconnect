package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/devshoe/gokiteconnect/models"
)

// TestLiveCandlesUpsertIntoPostgres needs a throwaway Postgres database in
// TRADEBOT_TEST_POSTGRES_DSN: it drops live_data.
func TestLiveCandlesUpsertIntoPostgres(t *testing.T) {
	dsn := os.Getenv("TRADEBOT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRADEBOT_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS live_data"); err != nil {
		t.Fatal(err)
	}
	store, err := NewLiveCandlesPostgresRepository(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	minute := time.Date(2026, time.October, 27, 9, 15, 0, 0, models.ExchangeLocation)
	first := []LiveCandle{
		{Token: 408065, InstrumentID: "NSE:INFY", Minute: minute, Open: 1500, High: 1504, Low: 1499, Close: 1502, FirstVolume: 1000, LastVolume: 1800},
		{Token: 2953217, InstrumentID: "NSE:TCS", Minute: minute.UTC(), Open: 4000, High: 4000, Low: 4000, Close: 4000, FirstVolume: 50, LastVolume: 50},
	}
	// A later batch of the same minute: its open and first volume give way
	// to the stored ones.
	second := []LiveCandle{
		{Token: 408065, InstrumentID: "NSE:INFY", Minute: minute, Open: 1501, High: 1506, Low: 1500, Close: 1505, FirstVolume: 1900, LastVolume: 2600},
		{Token: 408065, InstrumentID: "NSE:INFY", Minute: minute.Add(time.Minute), Open: 1505, High: 1505, Low: 1505, Close: 1505, FirstVolume: 2700, LastVolume: 2700},
	}
	for _, batch := range [][]LiveCandle{first, second} {
		if err := store.UpsertLiveCandles(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := db.QueryContext(ctx, "SELECT id, i, to_char(t, 'HH24:MI'), o, h, l, c, v, v0 FROM live_data ORDER BY id, t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type stored struct {
		id, interval, minute string
		o, h, l, c           float64
		v, v0                int64
	}
	var got []stored
	for rows.Next() {
		var row stored
		if err := rows.Scan(&row.id, &row.interval, &row.minute, &row.o, &row.h, &row.l, &row.c, &row.v, &row.v0); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	want := []stored{
		{"NSE:INFY", "1m", "09:15", 1500, 1506, 1499, 1505, 1600, 1000},
		{"NSE:INFY", "1m", "09:16", 1505, 1505, 1505, 1505, 0, 2700},
		{"NSE:TCS", "1m", "09:15", 4000, 4000, 4000, 4000, 0, 50},
	}
	if len(got) != len(want) {
		t.Fatalf("live_data = %+v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("row %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
