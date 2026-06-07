package db

import (
	"context"
	"database/sql"

	"github.com/paulmeier/kasas/internal/db/pg"
)

// PostgresStore is the Postgres-backed Store. It adapts the pg-generated
// queries to the canonical db types, which are structurally identical to the
// pg ones (enforced by the conversions below failing to compile if they drift).
type PostgresStore struct {
	pgQuerier
	db *sql.DB
}

// NewPostgresStore wraps a *sql.DB opened with the "pgx" driver.
func NewPostgresStore(database *sql.DB) *PostgresStore {
	return &PostgresStore{pgQuerier: pgQuerier{q: pg.New(database)}, db: database}
}

// RunInTx implements Store.
func (s *PostgresStore) RunInTx(ctx context.Context, fn func(Querier) error) error {
	return runInTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(pgQuerier{q: s.q.WithTx(tx)})
	})
}

// Ping implements Store.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements Store.
func (s *PostgresStore) Close() error { return s.db.Close() }

var _ Store = (*PostgresStore)(nil)

// pgQuerier implements Querier over the pg-generated queries.
type pgQuerier struct {
	q *pg.Queries
}

var _ Querier = pgQuerier{}

func (a pgQuerier) CompleteSyncLog(ctx context.Context, arg CompleteSyncLogParams) error {
	return a.q.CompleteSyncLog(ctx, pg.CompleteSyncLogParams(arg))
}

func (a pgQuerier) CountTransactions(ctx context.Context) (int64, error) {
	return a.q.CountTransactions(ctx)
}

func (a pgQuerier) CreateSyncLog(ctx context.Context, arg CreateSyncLogParams) (SyncLog, error) {
	row, err := a.q.CreateSyncLog(ctx, pg.CreateSyncLogParams(arg))
	return SyncLog(row), err
}

func (a pgQuerier) GetAccount(ctx context.Context, id string) (Account, error) {
	row, err := a.q.GetAccount(ctx, id)
	return Account(row), err
}

func (a pgQuerier) GetOrganization(ctx context.Context, id string) (Organization, error) {
	row, err := a.q.GetOrganization(ctx, id)
	return Organization(row), err
}

func (a pgQuerier) GetTransaction(ctx context.Context, id string) (Transaction, error) {
	row, err := a.q.GetTransaction(ctx, id)
	return Transaction(row), err
}

func (a pgQuerier) InsertTransaction(ctx context.Context, arg InsertTransactionParams) (int64, error) {
	return a.q.InsertTransaction(ctx, pg.InsertTransactionParams(arg))
}

func (a pgQuerier) UpdateTransactionFromSync(ctx context.Context, arg UpdateTransactionFromSyncParams) (int64, error) {
	return a.q.UpdateTransactionFromSync(ctx, pg.UpdateTransactionFromSyncParams(arg))
}

func (a pgQuerier) UpdateTransactionLabels(ctx context.Context, arg UpdateTransactionLabelsParams) (int64, error) {
	return a.q.UpdateTransactionLabels(ctx, pg.UpdateTransactionLabelsParams(arg))
}

func (a pgQuerier) ListLabeledTransactions(ctx context.Context) ([]ListLabeledTransactionsRow, error) {
	rows, err := a.q.ListLabeledTransactions(ctx)
	return mapSlice(rows, func(r pg.ListLabeledTransactionsRow) ListLabeledTransactionsRow {
		return ListLabeledTransactionsRow(r)
	}), err
}

// Schema extensions are stored as a JSON object column like labels. The param and
// row structs are all string, so they adapt by whole-struct cast / row cast.
func (a pgQuerier) UpdateTransactionExtensions(ctx context.Context, arg UpdateTransactionExtensionsParams) (int64, error) {
	return a.q.UpdateTransactionExtensions(ctx, pg.UpdateTransactionExtensionsParams(arg))
}

func (a pgQuerier) ListExtendedTransactions(ctx context.Context) ([]ListExtendedTransactionsRow, error) {
	rows, err := a.q.ListExtendedTransactions(ctx)
	return mapSlice(rows, func(r pg.ListExtendedTransactionsRow) ListExtendedTransactionsRow {
		return ListExtendedTransactionsRow(r)
	}), err
}

func (a pgQuerier) UpdateTransactionRelationships(ctx context.Context, arg UpdateTransactionRelationshipsParams) (int64, error) {
	return a.q.UpdateTransactionRelationships(ctx, pg.UpdateTransactionRelationshipsParams(arg))
}

func (a pgQuerier) ListRelatedTransactions(ctx context.Context) ([]ListRelatedTransactionsRow, error) {
	rows, err := a.q.ListRelatedTransactions(ctx)
	return mapSlice(rows, func(r pg.ListRelatedTransactionsRow) ListRelatedTransactionsRow {
		return ListRelatedTransactionsRow(r)
	}), err
}

// FilterTransactionsByLabelKey / ByLabelValue and DeleteLabelBy* push label
// querying down to SQL. The filter params are hand-mapped (not whole-struct cast)
// because pg emits int32 for limit/offset where db emits int64.
func (a pgQuerier) FilterTransactionsByLabelKey(ctx context.Context, arg FilterTransactionsByLabelKeyParams) ([]Transaction, error) {
	rows, err := a.q.FilterTransactionsByLabelKey(ctx, pg.FilterTransactionsByLabelKeyParams{
		LabelKey:  arg.LabelKey,
		AccountID: arg.AccountID,
		Since:     arg.Since,
		Until:     arg.Until,
		RowOffset: int32(arg.RowOffset),
		RowLimit:  int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) FilterTransactionsByLabelValue(ctx context.Context, arg FilterTransactionsByLabelValueParams) ([]Transaction, error) {
	rows, err := a.q.FilterTransactionsByLabelValue(ctx, pg.FilterTransactionsByLabelValueParams{
		LabelKey:   arg.LabelKey,
		LabelValue: arg.LabelValue,
		AccountID:  arg.AccountID,
		Since:      arg.Since,
		Until:      arg.Until,
		RowOffset:  int32(arg.RowOffset),
		RowLimit:   int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) DeleteLabelByKey(ctx context.Context, labelKey string) (int64, error) {
	return a.q.DeleteLabelByKey(ctx, labelKey)
}

func (a pgQuerier) DeleteLabelByValue(ctx context.Context, arg DeleteLabelByValueParams) (int64, error) {
	return a.q.DeleteLabelByValue(ctx, pg.DeleteLabelByValueParams(arg))
}

func (a pgQuerier) LatestSyncLog(ctx context.Context) (SyncLog, error) {
	row, err := a.q.LatestSyncLog(ctx)
	return SyncLog(row), err
}

func (a pgQuerier) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := a.q.ListAccounts(ctx)
	return mapSlice(rows, func(r pg.Account) Account { return Account(r) }), err
}

func (a pgQuerier) ListAccountsByOrg(ctx context.Context, orgID string) ([]Account, error) {
	rows, err := a.q.ListAccountsByOrg(ctx, orgID)
	return mapSlice(rows, func(r pg.Account) Account { return Account(r) }), err
}

func (a pgQuerier) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := a.q.ListOrganizations(ctx)
	return mapSlice(rows, func(r pg.Organization) Organization { return Organization(r) }), err
}

func (a pgQuerier) ListSyncLogs(ctx context.Context, rowLimit int64) ([]SyncLog, error) {
	rows, err := a.q.ListSyncLogs(ctx, int32(rowLimit))
	return mapSlice(rows, func(r pg.SyncLog) SyncLog { return SyncLog(r) }), err
}

func (a pgQuerier) ListTransactions(ctx context.Context, arg ListTransactionsParams) ([]Transaction, error) {
	rows, err := a.q.ListTransactions(ctx, pg.ListTransactionsParams{
		Since:     arg.Since,
		Until:     arg.Until,
		RowOffset: int32(arg.RowOffset),
		RowLimit:  int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) ListTransactionsByAccount(ctx context.Context, arg ListTransactionsByAccountParams) ([]Transaction, error) {
	rows, err := a.q.ListTransactionsByAccount(ctx, pg.ListTransactionsByAccountParams{
		AccountID: arg.AccountID,
		Since:     arg.Since,
		Until:     arg.Until,
		RowOffset: int32(arg.RowOffset),
		RowLimit:  int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) UpsertAccount(ctx context.Context, arg UpsertAccountParams) error {
	return a.q.UpsertAccount(ctx, pg.UpsertAccountParams(arg))
}

func (a pgQuerier) UpsertOrganization(ctx context.Context, arg UpsertOrganizationParams) error {
	return a.q.UpsertOrganization(ctx, pg.UpsertOrganizationParams(arg))
}

// Rule CRUD. The rules table columns are all int64/string, so the params and
// Rule rows are byte-identical to the pg ones and adapt by whole-struct cast.
func (a pgQuerier) CreateRule(ctx context.Context, arg CreateRuleParams) (Rule, error) {
	row, err := a.q.CreateRule(ctx, pg.CreateRuleParams(arg))
	return Rule(row), err
}

func (a pgQuerier) GetRule(ctx context.Context, id int64) (Rule, error) {
	row, err := a.q.GetRule(ctx, id)
	return Rule(row), err
}

func (a pgQuerier) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := a.q.ListRules(ctx)
	return mapSlice(rows, func(r pg.Rule) Rule { return Rule(r) }), err
}

func (a pgQuerier) ListEnabledRules(ctx context.Context) ([]Rule, error) {
	rows, err := a.q.ListEnabledRules(ctx)
	return mapSlice(rows, func(r pg.Rule) Rule { return Rule(r) }), err
}

func (a pgQuerier) UpdateRule(ctx context.Context, arg UpdateRuleParams) (int64, error) {
	return a.q.UpdateRule(ctx, pg.UpdateRuleParams(arg))
}

func (a pgQuerier) DeleteRule(ctx context.Context, id int64) (int64, error) {
	return a.q.DeleteRule(ctx, id)
}

// Event stream. The events table columns are all int64/string, so Event rows and
// InsertEventParams are byte-identical to the pg ones (whole-struct cast); only
// ListEventsAfter's row_limit needs the int32 hand-map (pg infers int32 for LIMIT).
func (a pgQuerier) InsertEvent(ctx context.Context, arg InsertEventParams) (Event, error) {
	row, err := a.q.InsertEvent(ctx, pg.InsertEventParams(arg))
	return Event(row), err
}

func (a pgQuerier) GetEventBySequence(ctx context.Context, id int64) (Event, error) {
	row, err := a.q.GetEventBySequence(ctx, id)
	return Event(row), err
}

func (a pgQuerier) ListEventsAfter(ctx context.Context, arg ListEventsAfterParams) ([]Event, error) {
	rows, err := a.q.ListEventsAfter(ctx, pg.ListEventsAfterParams{
		After:      arg.After,
		EventType:  arg.EventType,
		EntityType: arg.EntityType,
		EntityID:   arg.EntityID,
		RowLimit:   int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Event) Event { return Event(r) }), err
}

func (a pgQuerier) ListRecentEvents(ctx context.Context, rowLimit int64) ([]Event, error) {
	rows, err := a.q.ListRecentEvents(ctx, int32(rowLimit))
	return mapSlice(rows, func(r pg.Event) Event { return Event(r) }), err
}

func (a pgQuerier) DeleteEventsBefore(ctx context.Context, cutoff int64) (int64, error) {
	return a.q.DeleteEventsBefore(ctx, cutoff)
}

// Transaction history. The transaction_versions columns are all int64/string, so
// TransactionVersion rows and InsertTransactionVersionParams are byte-identical to
// the pg ones (whole-struct cast). ListTransactionVersions has no LIMIT, so unlike
// the events reads it needs no int32 hand-map; Count and DeleteBefore are plain
// int64 pass-throughs.
func (a pgQuerier) InsertTransactionVersion(ctx context.Context, arg InsertTransactionVersionParams) (TransactionVersion, error) {
	row, err := a.q.InsertTransactionVersion(ctx, pg.InsertTransactionVersionParams(arg))
	return TransactionVersion(row), err
}

func (a pgQuerier) ListTransactionVersions(ctx context.Context, transactionID string) ([]TransactionVersion, error) {
	rows, err := a.q.ListTransactionVersions(ctx, transactionID)
	return mapSlice(rows, func(r pg.TransactionVersion) TransactionVersion { return TransactionVersion(r) }), err
}

func (a pgQuerier) CountTransactionVersions(ctx context.Context, transactionID string) (int64, error) {
	return a.q.CountTransactionVersions(ctx, transactionID)
}

func (a pgQuerier) DeleteTransactionVersionsBefore(ctx context.Context, cutoff int64) (int64, error) {
	return a.q.DeleteTransactionVersionsBefore(ctx, cutoff)
}

// API keys. All columns are int64/string, so ApiKey rows and InsertApiKeyParams
// are byte-identical to the pg ones (whole-struct cast); no LIST has a LIMIT, so no
// int32 hand-map is needed.
func (a pgQuerier) InsertApiKey(ctx context.Context, arg InsertApiKeyParams) (ApiKey, error) {
	row, err := a.q.InsertApiKey(ctx, pg.InsertApiKeyParams(arg))
	return ApiKey(row), err
}

func (a pgQuerier) GetApiKeyByHash(ctx context.Context, keyHash string) (ApiKey, error) {
	row, err := a.q.GetApiKeyByHash(ctx, keyHash)
	return ApiKey(row), err
}

func (a pgQuerier) ListApiKeys(ctx context.Context) ([]ApiKey, error) {
	rows, err := a.q.ListApiKeys(ctx)
	return mapSlice(rows, func(r pg.ApiKey) ApiKey { return ApiKey(r) }), err
}

func (a pgQuerier) DeleteApiKey(ctx context.Context, id int64) (int64, error) {
	return a.q.DeleteApiKey(ctx, id)
}

func (a pgQuerier) UpdateApiKeyLastUsed(ctx context.Context, arg UpdateApiKeyLastUsedParams) error {
	return a.q.UpdateApiKeyLastUsed(ctx, pg.UpdateApiKeyLastUsedParams(arg))
}

// Webhooks. All columns are int64/string, so Webhook rows and the param structs are
// byte-identical to the pg ones (whole-struct cast); no LIST has a LIMIT.
func (a pgQuerier) InsertWebhook(ctx context.Context, arg InsertWebhookParams) (Webhook, error) {
	row, err := a.q.InsertWebhook(ctx, pg.InsertWebhookParams(arg))
	return Webhook(row), err
}

func (a pgQuerier) GetWebhook(ctx context.Context, id int64) (Webhook, error) {
	row, err := a.q.GetWebhook(ctx, id)
	return Webhook(row), err
}

func (a pgQuerier) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := a.q.ListWebhooks(ctx)
	return mapSlice(rows, func(r pg.Webhook) Webhook { return Webhook(r) }), err
}

func (a pgQuerier) ListEnabledWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := a.q.ListEnabledWebhooks(ctx)
	return mapSlice(rows, func(r pg.Webhook) Webhook { return Webhook(r) }), err
}

func (a pgQuerier) UpdateWebhook(ctx context.Context, arg UpdateWebhookParams) (int64, error) {
	return a.q.UpdateWebhook(ctx, pg.UpdateWebhookParams(arg))
}

func (a pgQuerier) UpdateWebhookSecret(ctx context.Context, arg UpdateWebhookSecretParams) (int64, error) {
	return a.q.UpdateWebhookSecret(ctx, pg.UpdateWebhookSecretParams(arg))
}

func (a pgQuerier) UpdateWebhookDeliveryStatus(ctx context.Context, arg UpdateWebhookDeliveryStatusParams) error {
	return a.q.UpdateWebhookDeliveryStatus(ctx, pg.UpdateWebhookDeliveryStatusParams(arg))
}

func (a pgQuerier) DeleteWebhook(ctx context.Context, id int64) (int64, error) {
	return a.q.DeleteWebhook(ctx, id)
}

// Plugins. All columns are int64/string, so Plugin rows and the param structs are
// structurally identical to the pg-generated ones and convert with a cast.
func (a pgQuerier) InsertPlugin(ctx context.Context, arg InsertPluginParams) (Plugin, error) {
	row, err := a.q.InsertPlugin(ctx, pg.InsertPluginParams(arg))
	return Plugin(row), err
}

func (a pgQuerier) GetPlugin(ctx context.Context, id int64) (Plugin, error) {
	row, err := a.q.GetPlugin(ctx, id)
	return Plugin(row), err
}

func (a pgQuerier) GetPluginByName(ctx context.Context, name string) (Plugin, error) {
	row, err := a.q.GetPluginByName(ctx, name)
	return Plugin(row), err
}

func (a pgQuerier) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := a.q.ListPlugins(ctx)
	return mapSlice(rows, func(r pg.Plugin) Plugin { return Plugin(r) }), err
}

func (a pgQuerier) ListEnabledPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := a.q.ListEnabledPlugins(ctx)
	return mapSlice(rows, func(r pg.Plugin) Plugin { return Plugin(r) }), err
}

func (a pgQuerier) SetPluginEnabled(ctx context.Context, arg SetPluginEnabledParams) (int64, error) {
	return a.q.SetPluginEnabled(ctx, pg.SetPluginEnabledParams(arg))
}

func (a pgQuerier) UpdatePluginManifest(ctx context.Context, arg UpdatePluginManifestParams) (int64, error) {
	return a.q.UpdatePluginManifest(ctx, pg.UpdatePluginManifestParams(arg))
}

func (a pgQuerier) UpdatePluginGrantedCapabilities(ctx context.Context, arg UpdatePluginGrantedCapabilitiesParams) (int64, error) {
	return a.q.UpdatePluginGrantedCapabilities(ctx, pg.UpdatePluginGrantedCapabilitiesParams(arg))
}

func (a pgQuerier) UpdatePluginConfig(ctx context.Context, arg UpdatePluginConfigParams) (int64, error) {
	return a.q.UpdatePluginConfig(ctx, pg.UpdatePluginConfigParams(arg))
}

func (a pgQuerier) UpdatePluginRunStatus(ctx context.Context, arg UpdatePluginRunStatusParams) error {
	return a.q.UpdatePluginRunStatus(ctx, pg.UpdatePluginRunStatusParams(arg))
}

func (a pgQuerier) DeletePlugin(ctx context.Context, id int64) (int64, error) {
	return a.q.DeletePlugin(ctx, id)
}

func mapSlice[T, U any](in []T, conv func(T) U) []U {
	out := make([]U, len(in))
	for i := range in {
		out[i] = conv(in[i])
	}
	return out
}
