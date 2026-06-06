// Command seed populates a kasas database with realistic demo data for local
// development and testing, so the dashboard and REST API have something to show
// without a real SimpleFIN bridge.
//
// It reads the same config file the server uses (default config.toml) to find
// the database, applies migrations, and upserts fixtures — so it is safe to
// re-run. Transaction dates are relative to "now", so the data always looks
// current.
//
// Usage:
//
//	go run ./scripts/seed [-config config.toml] [-extra N]
//	make seed
//
// -extra N appends N additional synthetic transactions (handy for exercising
// the dashboard's pagination / "Load more" once you exceed a page, e.g. -extra 60).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/migrations"
)

func main() {
	configPath := flag.String("config", "config.toml", "path to the kasas config file")
	extra := flag.Int("extra", 0, "append N extra synthetic transactions (for pagination testing)")
	flag.Parse()

	if err := run(*configPath, *extra); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(configPath string, extra int) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	database, dialect, dir, err := open(cfg.Database)
	if err != nil {
		return err
	}
	defer database.Close()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect(dialect); err != nil {
		return err
	}
	if err := goose.UpContext(context.Background(), database, dir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	var store db.Store = db.NewSQLiteStore(database)
	if cfg.Database.Driver == "postgres" {
		store = db.NewPostgresStore(database)
	}

	accounts, txns, err := seed(context.Background(), store, extra)
	if err != nil {
		return err
	}

	target := cfg.Database.Path
	if cfg.Database.Driver == "postgres" {
		target = "postgres"
	}
	log.Printf("seeded %d accounts and inserted %d new transactions into %s", accounts, txns, target)
	return nil
}

// open returns a database handle plus the goose dialect and migrations subdir
// for the configured driver.
func open(cfg config.Database) (database *sql.DB, dialect, dir string, err error) {
	switch cfg.Driver {
	case "postgres":
		database, err = sql.Open("pgx", cfg.DSN)
		return database, "postgres", "postgres", err
	default:
		if d := filepath.Dir(cfg.Path); d != "" && d != "." {
			if mkErr := os.MkdirAll(d, 0o755); mkErr != nil {
				return nil, "", "", mkErr
			}
		}
		dsn := "file:" + cfg.Path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
		database, err = sql.Open("sqlite", dsn)
		return database, "sqlite3", "sqlite", err
	}
}

// seed upserts the demo organizations, accounts, and transactions. It returns
// the number of accounts and the number of newly inserted transactions.
func seed(ctx context.Context, store db.Store, extra int) (accounts, inserted int, err error) {
	now := time.Now()
	syncedAt := now.Unix()
	day := func(n int) int64 { return now.AddDate(0, 0, -n).Unix() }

	tx := func(id, acct, amount, desc, payee string, pending bool, daysAgo int) db.InsertTransactionParams {
		p := int64(0)
		if pending {
			p = 1
		}
		return db.InsertTransactionParams{
			ID: id, AccountID: acct, Amount: amount, Pending: p,
			Date: day(daysAgo), Description: desc, Payee: payee, SyncedAt: syncedAt,
		}
	}

	orgs := []db.UpsertOrganizationParams{
		{ID: "chase.com", Domain: "chase.com", Name: "Chase", SfinUrl: "https://sfin.chase.com"},
		{ID: "amex.com", Domain: "amex.com", Name: "American Express", SfinUrl: "https://sfin.amex.com"},
	}
	accts := []db.UpsertAccountParams{
		{ID: "acct-checking", OrgID: "chase.com", Name: "Checking", Currency: "USD", Balance: "4283.19", BalanceDate: syncedAt, SyncedAt: syncedAt},
		{ID: "acct-savings", OrgID: "chase.com", Name: "Savings", Currency: "USD", Balance: "15250.00", BalanceDate: syncedAt, SyncedAt: syncedAt},
		{ID: "acct-card", OrgID: "amex.com", Name: "Credit Card", Currency: "USD", Balance: "-892.45", BalanceDate: syncedAt, SyncedAt: syncedAt},
	}
	txns := []db.InsertTransactionParams{
		tx("c1", "acct-checking", "-5.75", "Coffee", "Blue Bottle", false, 1),
		tx("c2", "acct-checking", "-128.99", "Groceries", "Whole Foods", false, 2),
		tx("c3", "acct-checking", "-34.20", "Pharmacy", "CVS", true, 2),
		tx("c4", "acct-checking", "-52.30", "Gas", "Shell", false, 5),
		tx("c5", "acct-checking", "2500.00", "Payroll", "Acme Corp", false, 7),
		tx("c6", "acct-checking", "-1850.00", "Rent", "Oakwood Apartments", false, 10),
		tx("c7", "acct-checking", "-22.40", "Hardware", "Ace Hardware", false, 14),
		tx("s1", "acct-savings", "500.00", "Transfer in", "Self", false, 4),
		tx("s2", "acct-savings", "12.45", "Interest", "Chase", false, 30),
		tx("a1", "acct-card", "-129.99", "Online Shopping", "Amazon", true, 1),
		tx("a2", "acct-card", "-64.00", "Restaurant", "Trattoria", false, 3),
		tx("a3", "acct-card", "-14.99", "Subscription", "Spotify", false, 6),
		tx("a4", "acct-card", "-98.50", "Utilities", "PG&E", false, 9),
		tx("a5", "acct-card", "-45.60", "Rideshare", "Uber", false, 12),
	}

	// A few demo labels so the dashboard's editable Labels column has content to
	// show. Labels are strict key:value pairs; "tag:" models a simple/flat label.
	// Applied after insert (the poller/insert path never sets labels).
	demoLabels := map[string]map[string]string{
		"c1": {"tag": "coffee"},
		"c2": {"category": "groceries"},
		"c4": {"category": "transport", "tag": "gas"},
		"c6": {"category": "housing", "tag": "rent"},
		"a2": {"category": "food", "tag": "dining"},
		"a3": {"category": "subscriptions"},
	}

	// Optional synthetic transactions, deterministic so re-runs are stable.
	if extra > 0 {
		payees := []string{"Target", "Costco", "Starbucks", "Apple", "Uber", "Walgreens", "Best Buy", "Trader Joe's", "Chevron", "Delta"}
		ids := []string{"acct-checking", "acct-card", "acct-savings"}
		for i := 0; i < extra; i++ {
			amount := fmt.Sprintf("-%d.%02d", 5+(i*7)%240, (i*13)%100)
			txns = append(txns, tx(
				fmt.Sprintf("gen-%05d", i),
				ids[i%len(ids)],
				amount,
				"Purchase",
				payees[i%len(payees)],
				i%17 == 0,
				i%60,
			))
		}
	}

	err = store.RunInTx(ctx, func(q db.Querier) error {
		for _, o := range orgs {
			if err := q.UpsertOrganization(ctx, o); err != nil {
				return err
			}
		}
		for _, a := range accts {
			if err := q.UpsertAccount(ctx, a); err != nil {
				return err
			}
		}
		for _, t := range txns {
			n, err := q.InsertTransaction(ctx, t)
			if err != nil {
				return err
			}
			inserted += int(n)
		}
		for id, labels := range demoLabels {
			enc, err := json.Marshal(labels)
			if err != nil {
				return err
			}
			if _, err := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: id, Labels: string(enc)}); err != nil {
				return err
			}
		}
		return nil
	})
	return len(accts), inserted, err
}
