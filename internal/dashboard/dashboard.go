// Package dashboard is a browser-side (WebAssembly) UI for browsing synced kasas
// accounts and transactions, built with go-app. It fetches data from the
// same-origin REST API and is served by Handler. Browsing is read-only, with one
// exception: each transaction's labels (key:value pairs) can be edited inline
// (add/remove, with typeahead suggestions), persisted via
// PUT /api/v1/transactions/{id}/labels.
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// defaultPageSize is the initially selected page size; pageSizeOptions are the
// choices offered by the "Show" dropdown.
const defaultPageSize = 50

var pageSizeOptions = []int{10, 20, 50, 100}

// sortColumn identifies which table column the transactions are sorted by.
type sortColumn int

const (
	sortByDate sortColumn = iota // default; matches the API's date-descending order
	sortByAccount
	sortByDescription
	sortByAmount
)

// allAccountsValue is the <option> value for the "All accounts" choice. It must
// be non-empty: go-app drops empty-string value attributes (see
// attributes.Set), and an <option> with no value attribute reports its text
// ("All accounts") as its value, which would then be sent as a bogus
// account_id filter. We map this sentinel back to "" (no filter) on change.
const allAccountsValue = "__all__"

// Routes registers the client-side routes. The WASM entrypoint calls this
// before app.RunWhenOnBrowser, and the server's Handler calls it too so these
// paths serve the SPA shell instead of 404ing.
func Routes() {
	app.Route("/", func() app.Composer { return &dashboardView{} })
	app.Route("/search", func() app.Composer { return &searchView{} })
	app.Route("/labels", func() app.Composer { return &labelsView{} })
	app.Route("/rules", func() app.Composer { return &rulesView{} })
	app.Route("/events", func() app.Composer { return &eventsView{} })
	app.Route("/webhooks", func() app.Composer { return &webhooksView{} })
	app.Route("/plugins", func() app.Composer { return &pluginsView{} })
	app.Route("/settings", func() app.Composer { return &settingsView{} })
}

// dashboardView is the root component: account overview + a filterable,
// paginated transactions table.
type dashboardView struct {
	app.Compo
	chrome               // shared sidebar + API client + version badge
	labelEditing         // inline label editor (state + handlers), shared with the Search page
	historyViewing       // per-transaction history modal, shared with the Search page
	provenanceViewing    // per-transaction provenance modal, shared with the Search page
	relationshipsViewing // per-transaction relationships modal, shared with the Search page

	accounts []account
	byID     map[string]account // account id -> account, for name lookup
	txns     []transaction      // full set for the current filter, unpaged

	selectedAccount string // "" means all accounts
	loading         bool
	errMsg          string

	// Sort + client-side pagination state. The dashboard fetches the whole
	// transaction set for the current account filter (txns), then sorts and
	// pages it in the browser so header clicks and page changes are instant.
	sortCol  sortColumn
	sortAsc  bool
	pageSize int // rows per page; one of pageSizeOptions
	page     int // current page, 0-based

	// Update banner state.
	update    updateStatus
	updating  bool
	updateMsg string // post-apply message (covers the restart window)
	updateErr string
}

func (v *dashboardView) OnMount(ctx app.Context) {
	v.loadChrome(ctx) // wires v.client, sidebar state, version badge
	v.initLabelEditing()
	v.initRelationshipsViewing()
	v.fetchHistory = v.client.transactionHistory
	v.fetchProvenance = v.client.transactionProvenance
	v.pageSize = defaultPageSize
	v.loadAccounts(ctx)
	v.reloadTransactions(ctx)
	v.loadVocab(ctx, v.client)
	v.loadUpdateStatus(ctx)
}

// initLabelEditing wires the shared label editor to this view's transaction
// slice and error field. Call after loadChrome (the setLabels hook needs
// v.client). txnByID returns the addressable slice element so optimistic edits
// land on the live row.
func (v *dashboardView) initLabelEditing() {
	v.txnByID = func(id string) *transaction {
		if i := v.txnIndex(id); i >= 0 {
			return &v.txns[i]
		}
		return nil
	}
	v.setLabels = v.client.setLabels
	v.reportError = func(msg string) { v.errMsg = msg }
}

// initRelationshipsViewing wires the shared relationships modal to this view's
// transaction set. It reuses txnByID (set by initLabelEditing) for the in-place
// row-indicator refresh, so call it after initLabelEditing.
func (v *dashboardView) initRelationshipsViewing() {
	v.fetchRelationships = v.client.transactionRelationships
	v.createRelationship = v.client.createTransactionRelationship
	v.deleteRelationship = v.client.deleteTransactionRelationship
	v.relAllTxns = func() []transaction { return v.txns }
	v.relTxnByID = v.txnByID
	v.relReportError = func(msg string) { v.errMsg = msg }
}

func originURL() string {
	if u := app.Window().URL(); u != nil {
		return u.Scheme + "://" + u.Host
	}
	return ""
}

func (v *dashboardView) loadAccounts(ctx app.Context) {
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
			ctx.Update()
		})
	})
}

func (v *dashboardView) reloadTransactions(ctx app.Context) {
	v.txns = nil
	v.page = 0
	v.loading = true
	v.fetchTransactions(ctx)
}

func (v *dashboardView) fetchTransactions(ctx app.Context) {
	acctID := v.selectedAccount
	ctx.Async(func() {
		txns, err := v.client.allTransactions(context.Background(), acctID)
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.txns = txns
			v.page = 0
			ctx.Update()
		})
	})
}

func (v *dashboardView) onAccountChange(ctx app.Context, _ app.Event) {
	val := ctx.JSSrc().Get("value").String()
	if val == allAccountsValue {
		val = ""
	}
	v.selectedAccount = val
	v.reloadTransactions(ctx)
}

// txnIndex returns the index of the transaction with the given id, or -1. The
// set is small (one filter's worth), so a linear scan is fine.
func (v *dashboardView) txnIndex(id string) int {
	for i := range v.txns {
		if v.txns[i].ID == id {
			return i
		}
	}
	return -1
}

func (v *dashboardView) onPageSizeChange(ctx app.Context, _ app.Event) {
	n, err := strconv.Atoi(ctx.JSSrc().Get("value").String())
	if err != nil || n <= 0 {
		return
	}
	v.pageSize = n
	v.page = 0
	ctx.Update()
}

// toggleSort selects a sort column, or flips direction when the column is
// already active. Sorting and paging happen client-side, so no refetch.
func (v *dashboardView) toggleSort(ctx app.Context, col sortColumn) {
	if v.sortCol == col {
		v.sortAsc = !v.sortAsc
	} else {
		v.sortCol = col
		v.sortAsc = defaultAscForColumn(col)
	}
	v.page = 0
	ctx.Update()
}

func (v *dashboardView) goToPage(ctx app.Context, p int) {
	if last := v.pageCount() - 1; p > last {
		p = last
	}
	if p < 0 {
		p = 0
	}
	if p == v.page {
		return
	}
	v.page = p
	ctx.Update()
}

// defaultAscForColumn is the initial direction when a column is first selected:
// text columns ascend (A→Z); date and amount descend (newest / largest first),
// which is what you usually want for transactions.
func defaultAscForColumn(col sortColumn) bool {
	switch col {
	case sortByAccount, sortByDescription:
		return true
	default: // date, amount
		return false
	}
}

// sortedTxns returns a copy of the full transaction set ordered by the active
// column and direction. The original slice is left untouched.
func (v *dashboardView) sortedTxns() []transaction {
	out := make([]transaction, len(v.txns))
	copy(out, v.txns)
	sort.SliceStable(out, func(i, j int) bool {
		return lessTxn(out[i], out[j], v.sortCol, v.sortAsc, v.accountName)
	})
	return out
}

// lessTxn orders two transactions by the given column and direction, with a
// stable id tiebreak for equal dates (matching the API's date,id order). nameOf
// resolves an account id to its display name for the Account column. Shared by
// the Dashboard and Search result tables.
func lessTxn(a, b transaction, col sortColumn, asc bool, nameOf func(string) string) bool {
	switch col {
	case sortByAccount:
		return cmpStr(nameOf(a.AccountID), nameOf(b.AccountID), asc)
	case sortByDescription:
		return cmpStr(displayDesc(a), displayDesc(b), asc)
	case sortByAmount:
		return cmpFloat(parseAmount(a.Amount), parseAmount(b.Amount), asc)
	default: // sortByDate
		if a.Date.Equal(b.Date) {
			return a.ID < b.ID // deterministic tiebreaker (matches the API)
		}
		return cmpTime(a.Date, b.Date, asc)
	}
}

// pageCountOf is the number of pages for total rows at the given page size
// (always >= 1). clampPage keeps a page index within [0, pageCount-1] so a
// shrinking result set can't leave the view past the end. Both are used by the
// Search results table.
func pageCountOf(total, size int) int {
	if size <= 0 {
		return 1
	}
	if n := (total + size - 1) / size; n > 1 {
		return n
	}
	return 1
}

func clampPage(page, total, size int) int {
	if last := pageCountOf(total, size) - 1; page > last {
		return last
	}
	if page < 0 {
		return 0
	}
	return page
}

// pageCount is the number of pages at the current page size (always >= 1).
func (v *dashboardView) pageCount() int {
	if v.pageSize <= 0 {
		return 1
	}
	if n := (len(v.txns) + v.pageSize - 1) / v.pageSize; n > 1 {
		return n
	}
	return 1
}

// clampedPage keeps the requested page within [0, pageCount-1] so a shrinking
// result set (e.g. after switching accounts) can't leave us past the end.
func (v *dashboardView) clampedPage() int {
	if last := v.pageCount() - 1; v.page > last {
		return last
	}
	if v.page < 0 {
		return 0
	}
	return v.page
}

// visibleTxns is the sorted slice for the current page.
func (v *dashboardView) visibleTxns() []transaction {
	sorted := v.sortedTxns()
	if v.pageSize <= 0 {
		return sorted
	}
	start := v.clampedPage() * v.pageSize
	if start >= len(sorted) {
		return nil
	}
	end := start + v.pageSize
	if end > len(sorted) {
		end = len(sorted)
	}
	return sorted[start:end]
}

// displayDesc is the text shown in the Description column: the payee, falling
// back to the raw description when there is no payee.
func displayDesc(t transaction) string {
	if t.Payee != "" {
		return t.Payee
	}
	return t.Description
}

// parseAmount turns a decimal amount string into a float for numeric sorting.
// Unparseable values sort as 0.
func parseAmount(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func cmpStr(a, b string, asc bool) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if asc {
		return a < b
	}
	return a > b
}

func cmpFloat(a, b float64, asc bool) bool {
	if asc {
		return a < b
	}
	return a > b
}

func cmpTime(a, b time.Time, asc bool) bool {
	if asc {
		return a.Before(b)
	}
	return a.After(b)
}

// loadUpdateStatus fetches the update banner state. It is best-effort: when the
// update check is disabled (404) or the request fails, the banner stays hidden.
func (v *dashboardView) loadUpdateStatus(ctx app.Context) {
	ctx.Async(func() {
		st, err := v.client.updateStatus(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return
			}
			v.update = st
			ctx.Update()
		})
	})
}

func (v *dashboardView) onApplyUpdate(ctx app.Context, _ app.Event) {
	if v.updating {
		return
	}
	v.updating = true
	v.updateErr = ""
	ctx.Update()
	ctx.Async(func() {
		res, err := v.client.applyUpdate(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.updating = false
			if err != nil {
				v.updateErr = err.Error()
				ctx.Update()
				return
			}
			v.updateMsg = res.Message
			v.update.Available = false
			ctx.Update()
			if res.Restarting {
				v.waitForRestart(ctx, res.Version)
			}
		})
	})
}

// waitForRestart polls until the server comes back reporting the new version,
// then reloads the page so the browser picks up the matching UI build.
func (v *dashboardView) waitForRestart(ctx app.Context, target string) {
	ctx.Async(func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			st, err := v.client.updateStatus(context.Background())
			if err == nil && st.Current == target {
				ctx.Dispatch(func(ctx app.Context) {
					app.Window().Get("location").Call("reload")
				})
				return
			}
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.updateMsg = "Updated to " + target + ". Reload the page to use the new version."
			ctx.Update()
		})
	})
}

func (v *dashboardView) onDismissUpdateErr(ctx app.Context, _ app.Event) {
	v.updateErr = ""
	ctx.Update()
}

func (v *dashboardView) Render() app.UI {
	return v.renderShell(navDashboard,
		v.renderUpdateBanner(),
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Transactions"),
			app.Span().Class("page-subtitle").Text("Your synced accounts and transactions"),
		),
		v.renderAccounts(),
		v.renderControls(),
		v.renderError(),
		v.renderTable(),
		v.renderFooter(),
		v.renderHistoryModal(),
		v.renderProvenanceModal(),
		v.renderRelationshipsModal(),
	)
}

// renderUpdateBanner shows a lightweight notice at the top of the dashboard when
// a newer release is available, with an optional "Update & restart" button.
func (v *dashboardView) renderUpdateBanner() app.UI {
	switch {
	case v.updateErr != "":
		return app.Div().Class("update-banner err").Body(
			app.Span().Class("update-text").Text("Update failed: "+v.updateErr),
			app.Button().Class("btn").Text("Dismiss").OnClick(v.onDismissUpdateErr),
		)
	case v.updateMsg != "":
		// Post-apply / restarting message.
		return app.Div().Class("update-banner").Body(
			app.Span().Class("update-text").Text(v.updateMsg),
		)
	case !v.update.Available:
		return app.Text("")
	}

	body := []app.UI{
		app.Span().Class("update-text").Body(
			app.Text("A new version of kasas is available: "),
			app.Span().Class("update-version").Text(v.update.Current+" → "+v.update.Latest),
		),
	}
	if v.update.URL != "" {
		body = append(body, app.A().Class("update-link").Href(v.update.URL).Target("_blank").Text("release notes"))
	}
	if v.update.CanApply {
		label := "Update & restart"
		if v.updating {
			label = "Updating…"
		}
		body = append(body, app.Button().
			Class("btn btn-update").
			Text(label).
			OnClick(v.onApplyUpdate).
			Disabled(v.updating))
	}
	return app.Div().Class("update-banner").Body(body...)
}

func (v *dashboardView) renderAccounts() app.UI {
	return app.Section().Class("cards").Body(
		app.Range(v.accounts).Slice(func(i int) app.UI {
			a := v.accounts[i]
			return app.Div().Class("card").Body(
				app.Div().Class("card-name").Text(a.Name),
				app.Div().Class("card-balance").Text(a.Balance+" "+a.Currency),
			)
		}),
	)
}

func (v *dashboardView) renderControls() app.UI {
	return app.Div().Class("controls").Body(
		app.Label().Class("control-label").Text("Account"),
		app.Select().Class("account-select").OnChange(v.onAccountChange).Body(
			app.Option().Value(allAccountsValue).Text("All accounts").Selected(v.selectedAccount == ""),
			app.Range(v.accounts).Slice(func(i int) app.UI {
				a := v.accounts[i]
				return app.Option().Value(a.ID).Text(a.Name).Selected(v.selectedAccount == a.ID)
			}),
		),
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

func (v *dashboardView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *dashboardView) renderTable() app.UI {
	if v.loading {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.txns) == 0 {
		return app.Div().Class("status").Text("No transactions.")
	}
	rows := v.visibleTxns()
	return app.Table().Class("txns").Body(
		app.THead().Body(
			app.Tr().Body(
				v.sortHeader("Date", sortByDate, ""),
				v.sortHeader("Account", sortByAccount, ""),
				v.sortHeader("Description", sortByDescription, ""),
				v.sortHeader("Amount", sortByAmount, "right"),
				// Labels and Extensions are plain headers: no sortColumn, not built
				// with sortHeader, so they are never sortable.
				app.Th().Class("labels-col").Text("Labels"),
				app.Th().Class("ext-col").Text("Extensions"),
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

// sortHeader renders a clickable column header with a direction arrow when it is
// the active sort column. extraClass carries presentation classes (e.g. "right").
func (v *dashboardView) sortHeader(label string, col sortColumn, extraClass string) app.UI {
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
		Body(
			app.Text(label),
			arrow,
		)
}

func (v *dashboardView) renderRow(t transaction) app.UI {
	amountClass := "amount pos"
	if strings.HasPrefix(strings.TrimSpace(t.Amount), "-") {
		amountClass = "amount neg"
	}
	return app.Tr().Body(
		app.Td().Text(t.Date.Format("2006-01-02")),
		app.Td().Text(v.accountName(t.AccountID)),
		app.Td().Text(displayDesc(t)),
		app.Td().Class(amountClass).Text(t.Amount),
		v.renderLabelsCell(t),
		renderExtensionsCell(t),
		app.Td().Class("row-actions").Body(
			pendingBadge(t.Pending),
			v.renderRelationshipsButton(t),
			v.renderProvenanceButton(t),
			v.renderHistoryButton(t),
		),
	)
}

// pendingBadge renders the "pending" badge, or nothing for a posted transaction.
// Shared by the Dashboard and Search result tables.
func pendingBadge(pending bool) app.UI {
	if !pending {
		return app.Text("")
	}
	return app.Span().Class("badge pending").Text("pending")
}

func (v *dashboardView) accountName(id string) string {
	if a, ok := v.byID[id]; ok {
		return a.Name
	}
	return id
}

// renderFooter shows the row range and, when the result set spans more than one
// page, the pagination controls.
func (v *dashboardView) renderFooter() app.UI {
	if v.loading || len(v.txns) == 0 {
		return app.Text("")
	}
	total := len(v.txns)
	pages := v.pageCount()
	page := v.clampedPage()

	start := page * v.pageSize
	end := start + v.pageSize
	if end > total {
		end = total
	}
	status := fmt.Sprintf("Showing %d–%d of %d", start+1, end, total)

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
			v.renderPageNumbers(page, pages),
			app.Button().Class("page-btn").Text("Next ›").
				Disabled(page >= pages-1).
				OnClick(func(ctx app.Context, _ app.Event) { v.goToPage(ctx, page+1) }),
		),
	)
}

// renderPageNumbers renders a windowed set of page buttons: the first and last
// page, the current page with one neighbour on each side, and an ellipsis to
// bridge any gap, so the control stays compact for long lists.
func (v *dashboardView) renderPageNumbers(page, pages int) app.UI {
	lo, hi := page-1, page+1
	if lo < 0 {
		lo = 0
	}
	if hi > pages-1 {
		hi = pages - 1
	}

	var items []app.UI
	pageButton := func(n int) app.UI {
		cls := "page-btn page-num"
		if n == page {
			cls += " active"
		}
		return app.Button().Class(cls).Text(strconv.Itoa(n + 1)).
			OnClick(func(ctx app.Context, _ app.Event) { v.goToPage(ctx, n) })
	}
	ellipsis := func() app.UI { return app.Span().Class("page-ellipsis").Text("…") }

	if lo > 0 {
		items = append(items, pageButton(0))
		if lo > 1 {
			items = append(items, ellipsis())
		}
	}
	for n := lo; n <= hi; n++ {
		items = append(items, pageButton(n))
	}
	if hi < pages-1 {
		if hi < pages-2 {
			items = append(items, ellipsis())
		}
		items = append(items, pageButton(pages-1))
	}
	return app.Div().Class("page-numbers").Body(items...)
}
