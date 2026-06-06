package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/paulmeier/kasas/internal/search"
)

// searchStorageKey is where the last-run search is persisted so the page shows
// the same results after navigating away and back (and across reloads). The
// query is re-run against freshly fetched transactions on mount rather than
// caching the result rows, so labels edited elsewhere stay current.
const searchStorageKey = "kasas.search"

// searchInputID is the stable DOM id of the query input (uncontrolled: its value
// is restored and cleared imperatively, mirroring the label editor's input).
const searchInputID = "search-input"

// persistedSearch is the LocalStorage payload for the Search page.
type persistedSearch struct {
	Query  string `json:"query"`
	Active bool   `json:"active"` // a search has actually been run (vs. a first visit)
}

// searchView is the Search page: a query box over the kasas query language
// (see internal/search), a scrollable syntax help modal, and a results table
// that reuses the Dashboard's sorting, pagination, and inline label editing.
//
// Matching runs in the browser against the full transaction set (fetched once,
// like the Dashboard), so results are instant. allTxns is the source of truth;
// results holds indices into it (in match order) so an inline label edit mutates
// the live row and survives a re-sort or re-render.
type searchView struct {
	app.Compo
	chrome         // shared sidebar + API client + version badge
	labelEditing   // inline label editor, shared with the Dashboard
	historyViewing // per-transaction history modal, shared with the Dashboard

	accounts []account
	byID     map[string]account
	allTxns  []transaction // every transaction, fetched once
	results  []int         // indices into allTxns that matched the last query

	query    string // current query text (mirrors the input box)
	parseErr string // syntax error from the last run ("" = none)
	errMsg   string // data-loading / save error
	searched bool   // a query has been run (controls the empty-state copy)
	loading  bool
	loaded   bool // allTxns has been fetched

	showHelp bool

	// auto-run of a restored query, once both accounts and transactions load.
	autoRun        bool
	didAutoRun     bool
	accountsLoaded bool

	// sort + client-side pagination (mirrors the Dashboard).
	sortCol  sortColumn
	sortAsc  bool
	pageSize int
	page     int
}

func (v *searchView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.initLabelEditing()
	v.fetchHistory = v.client.transactionHistory
	v.pageSize = defaultPageSize
	v.sortCol = sortByDate
	v.sortAsc = false

	var ps persistedSearch
	_ = ctx.LocalStorage().Get(searchStorageKey, &ps)
	v.query = ps.Query
	v.autoRun = ps.Active
	// Restore the query text into the (uncontrolled) input once it has rendered.
	ctx.Defer(func(app.Context) { setElementValue(searchInputID, v.query) })

	v.loadAccounts(ctx)
	v.loadVocab(ctx, v.client)
	v.loadTransactions(ctx)
}

// initLabelEditing wires the shared label editor to this view's transaction set.
// txnByID points into allTxns (the source of truth) so an edit is reflected by
// the next render even though results render from a sorted copy.
func (v *searchView) initLabelEditing() {
	v.txnByID = func(id string) *transaction {
		for i := range v.allTxns {
			if v.allTxns[i].ID == id {
				return &v.allTxns[i]
			}
		}
		return nil
	}
	v.setLabels = v.client.setLabels
	v.reportError = func(msg string) { v.errMsg = msg }
}

func (v *searchView) loadAccounts(ctx app.Context) {
	ctx.Async(func() {
		accts, err := v.client.accounts(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.accounts = accts
			v.byID = make(map[string]account, len(accts))
			for _, a := range accts {
				v.byID[a.ID] = a
			}
			v.accountsLoaded = true
			v.maybeAutoRun(ctx)
			ctx.Update()
		})
	})
}

func (v *searchView) loadTransactions(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		txns, err := v.client.allTransactions(context.Background(), "")
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			v.loaded = true
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.allTxns = txns
			v.maybeAutoRun(ctx)
			ctx.Update()
		})
	})
}

// maybeAutoRun runs the restored query once, after both accounts (for account:
// matching) and transactions have loaded.
func (v *searchView) maybeAutoRun(ctx app.Context) {
	if !v.autoRun || v.didAutoRun || !v.loaded || !v.accountsLoaded {
		return
	}
	v.didAutoRun = true
	v.runSearch(ctx)
}

// runSearch parses the current query and recomputes the matching results. A
// syntax error is shown inline and leaves the previous results untouched.
func (v *searchView) runSearch(ctx app.Context) {
	v.searched = true
	_ = ctx.LocalStorage().Set(searchStorageKey, persistedSearch{Query: v.query, Active: true})

	q, err := search.Parse(v.query)
	if err != nil {
		v.parseErr = err.Error()
		ctx.Update()
		return
	}
	v.parseErr = ""

	results := make([]int, 0, len(v.allTxns))
	for i := range v.allTxns {
		if q.Match(v.toRecord(v.allTxns[i])) {
			results = append(results, i)
		}
	}
	v.results = results
	v.page = 0
	ctx.Update()
}

// toRecord adapts a transaction into the search engine's neutral Record,
// resolving the account name and parsing the amount for numeric comparisons.
func (v *searchView) toRecord(t transaction) search.Record {
	return search.Record{
		ID:          t.ID,
		AccountID:   t.AccountID,
		AccountName: v.accountName(t.AccountID),
		Amount:      parseAmount(t.Amount),
		AmountRaw:   t.Amount,
		Pending:     t.Pending,
		Date:        t.Date,
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      t.Labels,
		SyncedAt:    t.SyncedAt,
	}
}

func (v *searchView) accountName(id string) string {
	if a, ok := v.byID[id]; ok {
		return a.Name
	}
	return id
}

// --- input + control handlers ---

func (v *searchView) onQueryInput(ctx app.Context, _ app.Event) {
	v.query = ctx.JSSrc().Get("value").String()
}

func (v *searchView) onQueryKeyDown(ctx app.Context, e app.Event) {
	if e.Get("key").String() == "Enter" {
		e.PreventDefault()
		v.query = ctx.JSSrc().Get("value").String()
		v.runSearch(ctx)
	}
}

func (v *searchView) onRun(ctx app.Context, _ app.Event) { v.runSearch(ctx) }

func (v *searchView) onClear(ctx app.Context, _ app.Event) {
	v.query = ""
	setElementValue(searchInputID, "")
	v.results = nil
	v.searched = false
	v.parseErr = ""
	ctx.LocalStorage().Del(searchStorageKey)
	ctx.Update()
}

func (v *searchView) toggleHelp(ctx app.Context, _ app.Event) {
	v.showHelp = !v.showHelp
	ctx.Update()
}

func (v *searchView) closeHelp(ctx app.Context, _ app.Event) {
	v.showHelp = false
	ctx.Update()
}

// setElementValue imperatively sets a DOM input's value by id (go-app drops
// empty value attributes, so clearing/restoring is done directly on the node).
func setElementValue(id, val string) {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	el := doc.Call("getElementById", id)
	if !el.Truthy() {
		return
	}
	el.Set("value", val)
}

// --- sort + pagination (mirrors the Dashboard, over results) ---

func (v *searchView) toggleSort(ctx app.Context, col sortColumn) {
	if v.sortCol == col {
		v.sortAsc = !v.sortAsc
	} else {
		v.sortCol = col
		v.sortAsc = defaultAscForColumn(col)
	}
	v.page = 0
	ctx.Update()
}

func (v *searchView) onPageSizeChange(ctx app.Context, _ app.Event) {
	n, err := strconv.Atoi(ctx.JSSrc().Get("value").String())
	if err != nil || n <= 0 {
		return
	}
	v.pageSize = n
	v.page = 0
	ctx.Update()
}

func (v *searchView) goToPage(ctx app.Context, p int) {
	p = clampPage(p, len(v.results), v.pageSize)
	if p == v.page {
		return
	}
	v.page = p
	ctx.Update()
}

// sortedResults resolves the matched indices into transactions and orders them
// by the active column. A fresh copy each render keeps allTxns the single source
// of truth, so inline label edits show up on the next render.
func (v *searchView) sortedResults() []transaction {
	out := make([]transaction, len(v.results))
	for i, idx := range v.results {
		out[i] = v.allTxns[idx]
	}
	sort.SliceStable(out, func(i, j int) bool {
		return lessTxn(out[i], out[j], v.sortCol, v.sortAsc, v.accountName)
	})
	return out
}

func (v *searchView) visibleResults() []transaction {
	sorted := v.sortedResults()
	if v.pageSize <= 0 {
		return sorted
	}
	start := clampPage(v.page, len(sorted), v.pageSize) * v.pageSize
	if start >= len(sorted) {
		return nil
	}
	end := start + v.pageSize
	if end > len(sorted) {
		end = len(sorted)
	}
	return sorted[start:end]
}

// --- rendering ---

func (v *searchView) Render() app.UI {
	return v.renderShell(navSearch,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Search"),
			app.Span().Class("page-subtitle").Text("Query any field and any combination of labels"),
		),
		v.renderSearchBar(),
		v.renderParseError(),
		v.renderError(),
		v.renderControls(),
		v.renderResults(),
		v.renderFooter(),
		v.renderHelpModal(),
		v.renderHistoryModal(),
	)
}

// renderControls shows the page-size selector above the results (only once a
// search has returned rows), mirroring the Dashboard's "Show" control.
func (v *searchView) renderControls() app.UI {
	if !v.loaded || !v.searched || len(v.results) == 0 {
		return app.Text("")
	}
	return app.Div().Class("controls").Body(
		app.Span().Class("controls-spacer"),
		app.Label().Class("control-label").Text("Show"),
		app.Select().Class("pagesize-select").OnChange(v.onPageSizeChange).Body(
			app.Range(pageSizeOptions).Slice(func(i int) app.UI {
				n := pageSizeOptions[i]
				s := strconv.Itoa(n)
				return app.Option().Value(s).Text(s).Selected(v.pageSize == n)
			}),
		),
	)
}

func (v *searchView) renderSearchBar() app.UI {
	return app.Div().Class("search-bar").Body(
		app.Input().
			ID(searchInputID).
			Class("search-input").
			Type("text").
			Placeholder(`Search… e.g.  coffee amount:<0 date:2024 label:category=food`).
			OnInput(v.onQueryInput).
			OnKeyDown(v.onQueryKeyDown),
		app.Button().Class("btn btn-primary search-run").Text("Search").OnClick(v.onRun),
		app.Button().Class("btn search-clear").Title("Clear search").Text("Clear").OnClick(v.onClear),
		app.Button().Class("btn search-help").Title("Search syntax help").Text("? Help").OnClick(v.toggleHelp),
	)
}

func (v *searchView) renderParseError() app.UI {
	if v.parseErr == "" {
		return app.Text("")
	}
	return app.Div().Class("search-parse-error").Body(
		app.Span().Class("search-parse-label").Text("Invalid query: "),
		app.Span().Text(v.parseErr),
	)
}

func (v *searchView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *searchView) renderResults() app.UI {
	if !v.loaded {
		if v.loading {
			return app.Div().Class("status").Text("Loading transactions…")
		}
		return app.Text("")
	}
	if !v.searched {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("Search your transactions"),
			app.P().Class("empty-hint").Text("Type a query above and press Search. Click “? Help” for the full syntax."),
		)
	}
	if len(v.results) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No matching transactions."),
			app.P().Class("empty-hint").Text("Try loosening your query, or check the syntax with “? Help”."),
		)
	}
	rows := v.visibleResults()
	return app.Table().Class("txns").Body(
		app.THead().Body(
			app.Tr().Body(
				v.sortHeader("Date", sortByDate, ""),
				v.sortHeader("Account", sortByAccount, ""),
				v.sortHeader("Description", sortByDescription, ""),
				v.sortHeader("Amount", sortByAmount, "right"),
				app.Th().Class("labels-col").Text("Labels"),
				app.Th().Text(""),
			),
		),
		app.TBody().Body(
			app.Range(rows).Slice(func(i int) app.UI {
				return v.renderRow(rows[i])
			}),
		),
	)
}

func (v *searchView) sortHeader(label string, col sortColumn, extraClass string) app.UI {
	cls := "sortable"
	if extraClass != "" {
		cls += " " + extraClass
	}
	arrow := app.Text("")
	if v.sortCol == col {
		glyph := " ▼"
		if v.sortAsc {
			glyph = " ▲"
		}
		arrow = app.Span().Class("sort-arrow").Text(glyph)
	}
	return app.Th().Class(cls).
		OnClick(func(ctx app.Context, _ app.Event) { v.toggleSort(ctx, col) }).
		Body(app.Text(label), arrow)
}

func (v *searchView) renderRow(t transaction) app.UI {
	amountClass := "amount pos"
	if strings.HasPrefix(strings.TrimSpace(t.Amount), "-") {
		amountClass = "amount neg"
	}
	return app.Tr().Body(
		app.Td().Text(t.Date.Format("2006-01-02")),
		app.Td().Text(v.accountName(t.AccountID)),
		app.Td().Text(displayDesc(t)),
		app.Td().Class(amountClass).Text(t.Amount),
		v.renderLabelsCell(t), // promoted from labelEditing
		app.Td().Class("row-actions").Body(
			pendingBadge(t.Pending),
			v.renderHistoryButton(t), // promoted from historyViewing
		),
	)
}

func (v *searchView) renderFooter() app.UI {
	if !v.loaded || !v.searched || len(v.results) == 0 {
		return app.Text("")
	}
	total := len(v.results)
	pages := pageCountOf(total, v.pageSize)
	page := clampPage(v.page, total, v.pageSize)

	start := page * v.pageSize
	end := start + v.pageSize
	if end > total {
		end = total
	}
	status := fmt.Sprintf("Showing %d–%d of %d match", start+1, end, total)
	if total != 1 {
		status += "es"
	}

	if pages <= 1 {
		return app.Div().Class("pagination").Body(
			app.Span().Class("page-status").Text(status),
		)
	}
	return app.Div().Class("pagination").Body(
		app.Span().Class("page-status").Text(status),
		app.Div().Class("pager").Body(
			app.Button().Class("page-btn").Text("‹ Prev").
				Disabled(page == 0).
				OnClick(func(ctx app.Context, _ app.Event) { v.goToPage(ctx, page-1) }),
			app.Span().Class("page-status").Text(fmt.Sprintf(" Page %d / %d ", page+1, pages)),
			app.Button().Class("page-btn").Text("Next ›").
				Disabled(page >= pages-1).
				OnClick(func(ctx app.Context, _ app.Event) { v.goToPage(ctx, page+1) }),
		),
	)
}

// renderHelpModal renders the scrollable syntax reference. The backdrop closes
// it; clicks inside the panel do not (the panel stops click propagation).
func (v *searchView) renderHelpModal() app.UI {
	return renderSyntaxModal(v.showHelp, "Search syntax", v.closeHelp)
}

// renderSyntaxModal renders the shared, scrollable query-syntax reference. It is
// used by the Search page and the Rules page (a rule's condition is a search
// query), so the syntax help lives in one place. The backdrop closes it; clicks
// inside the panel do not.
func renderSyntaxModal(show bool, title string, onClose app.EventHandler) app.UI {
	if !show {
		return app.Text("")
	}
	return app.Div().Class("modal-overlay").OnClick(onClose).Body(
		app.Div().Class("modal").
			OnClick(func(_ app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.H2().Class("modal-title").Text(title),
					app.Button().Class("modal-close").Title("Close").Text("×").OnClick(onClose),
				),
				app.Div().Class("modal-body").Body(searchSyntaxHelp()...),
			),
	)
}

// searchSyntaxHelp is the body of the query-syntax reference, shared by the
// Search and Rules help modals.
func searchSyntaxHelp() []app.UI {
	return []app.UI{
		app.P().Class("help-intro").Text(
			"A query is a set of terms. Adjacent terms must all match (AND). " +
				"Matching is case-insensitive. Leave the box empty to list everything."),

		helpSection("Free text", []helpEntry{
			{"coffee", "match anywhere: description, payee, memo, account, id, or a label"},
			{`"whole foods"`, "quote to match an exact phrase containing spaces"},
		}),
		helpSection("Fields", []helpEntry{
			{"description:rent", "substring of the description"},
			{"payee:\"trader joe\"", "substring of the payee (quote for spaces)"},
			{"memo:gift", "substring of the memo"},
			{"account:checking", "account name contains"},
			{"id:abc123", "transaction id contains"},
			{"pending:true", "pending (true/false)"},
		}),
		helpSection("Amounts", []helpEntry{
			{"amount:>50", "greater than (also >=, <, <=, =, !=)"},
			{"amount:<0", "outflows (negative amounts)"},
			{"amount:10..50", "inclusive range (also -50..-10, ..50, 1000..)"},
		}),
		helpSection("Dates", []helpEntry{
			{"date:2024", "anything in 2024 (also 2024-03, 2024-03-15)"},
			{"date:>=2024-06-01", "on/after a date (also >, <, <=)"},
			{"date:2024-01..2024-06", "date range"},
		}),
		helpSection("Labels", []helpEntry{
			{"label:category=food", "key equals value"},
			{"category:food", "shorthand for the above"},
			{"label:category", "key present (any value)"},
			{"label:store~whole", "value contains"},
			{"label:category!=food", "value not equal"},
		}),
		helpSection("Combining", []helpEntry{
			{"food coffee", "implicit AND — both must match"},
			{"category:food OR category:rent", "either matches (also |)"},
			{"-pending:true", "exclude (also NOT)"},
			{"(a OR b) amount:<0", "group with parentheses"},
		}),
		app.P().Class("help-example").Text(
			"Example:  coffee amount:<0 date:2024 -label:reimbursed"),
	}
}

type helpEntry struct{ syntax, desc string }

func helpSection(title string, entries []helpEntry) app.UI {
	rows := make([]app.UI, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, app.Div().Class("help-row").Body(
			app.Code().Class("help-syntax").Text(e.syntax),
			app.Span().Class("help-desc").Text(e.desc),
		))
	}
	return app.Div().Class("help-section").Body(
		append([]app.UI{app.H3().Class("help-title").Text(title)}, rows...)...,
	)
}
