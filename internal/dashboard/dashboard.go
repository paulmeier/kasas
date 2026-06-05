// Package dashboard is a browser-side (WebAssembly) UI for browsing synced kasas
// accounts and transactions, built with go-app. It fetches data from the
// same-origin REST API and is served by Handler. Browsing is read-only, with one
// exception: each transaction's tags can be edited inline (add/remove, with
// typeahead suggestions), which it persists via PUT /api/v1/transactions/{id}/tags.
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
// before app.RunWhenOnBrowser.
func Routes() {
	app.Route("/", func() app.Composer { return &dashboardView{} })
}

// dashboardView is the root component: account overview + a filterable,
// paginated transactions table.
type dashboardView struct {
	app.Compo

	client *apiClient

	accounts []account
	byID     map[string]account // account id -> account, for name lookup
	txns     []transaction      // full set for the current filter, unpaged

	selectedAccount string // "" means all accounts
	loading         bool
	errMsg          string

	// Tag editing state. allTags is the global tag vocabulary (from
	// /api/v1/tags) that powers the typeahead. Only one row's add-tag input is
	// active at a time: tagEditID is that row's transaction id ("" = none) and
	// tagDraft mirrors the input's text so suggestions can be filtered. The add
	// input is uncontrolled (its DOM value is the source of truth and is cleared
	// imperatively), so tagDraft is only used to decide what to suggest.
	allTags   []string
	tagDraft  string
	tagEditID string

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

	// Build version shown in the corner badge, fetched from the server's
	// /web/version (the same "<binary-version>-<wasmhash>" string go-app uses as
	// its service-worker cache key), so it changes exactly when the served UI
	// changes — a quick way to confirm the browser is running a fresh build.
	version string
}

func (v *dashboardView) OnMount(ctx app.Context) {
	v.client = newAPIClient(originURL())
	v.pageSize = defaultPageSize
	v.loadAccounts(ctx)
	v.reloadTransactions(ctx)
	v.loadTags(ctx)
	v.loadUpdateStatus(ctx)
	v.loadVersion(ctx)
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

// loadTags fetches the global tag vocabulary for the typeahead. Best-effort: if
// it fails the suggestions are simply empty until the next successful load.
func (v *dashboardView) loadTags(ctx app.Context) {
	ctx.Async(func() {
		tags, err := v.client.tags(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return
			}
			v.allTags = tags
			ctx.Update()
		})
	})
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

// addTag adds a typed/picked tag to a transaction. Matching is case-insensitive,
// so a tag already present (in any casing) is a no-op. The input is cleared and
// re-focused afterward (via clearTagInput) so the user can keep adding tags.
func (v *dashboardView) addTag(ctx app.Context, id, raw string) {
	tag := strings.TrimSpace(raw)
	if i := v.txnIndex(id); tag != "" && i >= 0 && !containsFold(v.txns[i].Tags, tag) {
		next := append(append([]string{}, v.txns[i].Tags...), tag)
		v.saveTags(ctx, id, next)
	}
	// Clear + refocus last, so the focus lands after the chip-insert re-render.
	v.clearTagInput(ctx, id)
}

// containsFold reports whether tags contains s, comparing case-insensitively.
func containsFold(tags []string, s string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, s) {
			return true
		}
	}
	return false
}

// removeTag drops a tag from a transaction (case-insensitive match).
func (v *dashboardView) removeTag(ctx app.Context, id, tag string) {
	i := v.txnIndex(id)
	if i < 0 {
		return
	}
	next := make([]string, 0, len(v.txns[i].Tags))
	for _, existing := range v.txns[i].Tags {
		if !strings.EqualFold(existing, tag) {
			next = append(next, existing)
		}
	}
	v.saveTags(ctx, id, next)
}

// saveTags optimistically applies the new tag set to the row, then persists it.
// On failure it reverts and surfaces the error; on success it adopts the
// server-normalized set and folds any new tags into the suggestion vocabulary.
func (v *dashboardView) saveTags(ctx app.Context, id string, next []string) {
	i := v.txnIndex(id)
	if i < 0 {
		return
	}
	prev := v.txns[i].Tags
	v.txns[i].Tags = next
	v.mergeVocab(next)
	ctx.Update()

	ctx.Async(func() {
		saved, err := v.client.setTags(context.Background(), id, next)
		ctx.Dispatch(func(ctx app.Context) {
			j := v.txnIndex(id)
			if err != nil {
				if j >= 0 {
					v.txns[j].Tags = prev // revert the optimistic update
				}
				v.errMsg = "Failed to save tags: " + err.Error()
				ctx.Update()
				return
			}
			// Adopt the server-normalized set, but only re-render when it
			// actually differs from the optimistic one. Skipping the no-op render
			// avoids recreating the add-tag input and stealing its focus while the
			// user is still tagging the row.
			if j >= 0 && !equalTags(v.txns[j].Tags, saved) {
				v.txns[j].Tags = saved
				v.mergeVocab(saved)
				ctx.Update()
				return
			}
			v.mergeVocab(saved)
		})
	})
}

// equalTags reports whether two tag slices are identical (order-sensitive).
func equalTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mergeVocab folds tags into allTags (case-insensitive), keeping it sorted, so
// freshly created tags are immediately suggestable without a refetch.
func (v *dashboardView) mergeVocab(tags []string) {
	for _, tag := range tags {
		found := false
		for _, existing := range v.allTags {
			if strings.EqualFold(existing, tag) {
				found = true
				break
			}
		}
		if !found {
			v.allTags = append(v.allTags, tag)
		}
	}
	sort.Slice(v.allTags, func(i, j int) bool {
		return strings.ToLower(v.allTags[i]) < strings.ToLower(v.allTags[j])
	})
}

// clearTagInput resets the draft and clears the row's uncontrolled add-tag input
// imperatively. go-app v10 drops empty value attributes, so a controlled
// Value("") would not clear the field — set the DOM value directly instead.
// After the re-render (which can recreate the input node and drop focus), it
// re-focuses the input so the user can keep adding tags and a later click-away
// still closes the editor via blur.
func (v *dashboardView) clearTagInput(ctx app.Context, id string) {
	v.tagDraft = ""
	v.focusTagInput(ctx, id, true)
	ctx.Update()
	// Re-assert focus after the re-render. With the chips in their own container
	// the input node is reused (so focus is usually preserved already), but this
	// keeps it robust if go-app recreates it.
	ctx.Defer(func(ctx app.Context) {
		if v.tagEditID == id {
			v.focusTagInput(ctx, id, false)
		}
	})
}

// focusTagInput focuses a row's add-tag input by id, optionally clearing its
// value first. No-op when the element is absent (e.g. not currently editing).
func (v *dashboardView) focusTagInput(_ app.Context, id string, clear bool) {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	el := doc.Call("getElementById", tagInputID(id))
	if !el.Truthy() {
		return
	}
	if clear {
		el.Set("value", "")
	}
	el.Call("focus")
}

// onTagsCellClick opens the add-tag editor for a row when its Tags cell is
// clicked in empty space, then focuses the freshly-rendered input. Clicks on the
// cell's interactive children (remove × buttons, the input, suggestion buttons)
// are ignored so they keep their own behavior.
func (v *dashboardView) onTagsCellClick(ctx app.Context, t transaction, e app.Event) {
	if target := e.Get("target"); target.Truthy() {
		switch target.Get("tagName").String() {
		case "BUTTON", "INPUT":
			return
		}
	}
	if v.tagEditID == t.ID {
		return // already editing this row
	}
	v.tagEditID = t.ID
	v.tagDraft = ""
	ctx.Update()
	// Focus after the input has been rendered into the DOM.
	ctx.Defer(func(ctx app.Context) {
		if v.tagEditID == t.ID {
			v.focusTagInput(ctx, t.ID, false)
		}
	})
}

func (v *dashboardView) onTagInput(ctx app.Context, id string) {
	v.tagEditID = id
	v.tagDraft = ctx.JSSrc().Get("value").String()
	ctx.Update()
}

func (v *dashboardView) onTagKeyDown(ctx app.Context, e app.Event, id string) {
	switch e.Get("key").String() {
	case "Enter":
		e.PreventDefault()
		v.addTag(ctx, id, ctx.JSSrc().Get("value").String())
	case "Escape":
		// Closing the editor removes the input, which drops focus.
		v.tagEditID = ""
		v.tagDraft = ""
		ctx.Update()
	}
}

// onTagBlur hides the suggestions when the input loses focus. Suggestion picks
// use mousedown + preventDefault, which keeps the input focused, so blur does
// not fire on a pick and the click is not lost.
func (v *dashboardView) onTagBlur(ctx app.Context) {
	v.tagEditID = ""
	v.tagDraft = ""
	ctx.Update()
}

// filterTagSuggestions returns vocabulary tags matching the draft
// (case-insensitive substring), excluding those already applied to the row. The
// list is capped so the dropdown stays compact.
func filterTagSuggestions(all []string, draft string, applied []string) []string {
	const maxSuggestions = 8
	d := strings.ToLower(strings.TrimSpace(draft))
	if d == "" {
		return nil
	}
	isApplied := func(tag string) bool {
		for _, a := range applied {
			if strings.EqualFold(a, tag) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, tag := range all {
		if isApplied(tag) {
			continue
		}
		if strings.Contains(strings.ToLower(tag), d) {
			out = append(out, tag)
			if len(out) >= maxSuggestions {
				break
			}
		}
	}
	return out
}

// tagInputID is the stable DOM id of a row's add-tag input, used to clear it.
func tagInputID(txnID string) string { return "tag-input-" + txnID }

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
	asc := v.sortAsc
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch v.sortCol {
		case sortByAccount:
			return cmpStr(v.accountName(a.AccountID), v.accountName(b.AccountID), asc)
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
	})
	return out
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

// loadVersion fetches the build version for the corner badge. Best-effort: the
// badge stays hidden if the request fails.
func (v *dashboardView) loadVersion(ctx app.Context) {
	ctx.Async(func() {
		ver, err := v.client.buildVersion(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return
			}
			v.version = ver
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
	return app.Main().Class("page").Body(
		v.renderUpdateBanner(),
		app.Header().Class("topbar").Body(
			app.Img().Class("logo").Src("/web/logo.png").Alt("kasas logo"),
			app.H1().Class("brand").Text("kasas"),
			app.Span().Class("subtitle").Text("transactions"),
		),
		v.renderAccounts(),
		v.renderControls(),
		v.renderError(),
		v.renderTable(),
		v.renderFooter(),
		v.renderVersion(),
	)
}

// renderVersion shows the build version in a fixed bottom-left corner badge. The
// value is go-app's GOAPP_VERSION — the binary version with a short hash of the
// served UI mixed in (the service-worker cache key) — so a changed badge is a
// quick confirmation that the browser is running a fresh build, not a cached one.
func (v *dashboardView) renderVersion() app.UI {
	if v.version == "" {
		return app.Text("")
	}
	return app.Div().
		Class("version-badge").
		Title("kasas build version (service-worker cache key)").
		Text(v.version)
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
				// Tags is intentionally a plain header: it has no sortColumn and
				// is not built with sortHeader, so it is never sortable.
				app.Th().Class("tags-col").Text("Tags"),
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
		v.renderTagsCell(t),
		app.Td().Body(v.pendingBadge(t.Pending)),
	)
}

// renderTagsCell renders the Tags cell: each current tag as a chip with a remove
// (×) button. The add-tag input is hidden until the cell is clicked (see
// onTagsCellClick) — so untagged rows are just empty, clickable space rather than
// a wall of "add tag…" boxes. While editing, typing shows a typeahead dropdown.
func (v *dashboardView) renderTagsCell(t transaction) app.UI {
	editing := v.tagEditID == t.ID

	chips := make([]app.UI, 0, len(t.Tags))
	for _, tag := range t.Tags {
		tag := tag // capture for the closure
		chips = append(chips, app.Span().Class("tag-chip").Body(
			app.Span().Class("tag-label").Text(tag),
			app.Button().Type("button").Class("tag-remove").Title("Remove "+tag).Text("×").
				OnClick(func(ctx app.Context, _ app.Event) { v.removeTag(ctx, t.ID, tag) }),
		))
	}

	// Chips live in their own container so adding/removing one never touches the
	// add-tag input that follows it. Keeping the input at a stable DOM position
	// lets go-app reuse the node across re-renders, which preserves its focus
	// while the user keeps tagging the row.
	body := []app.UI{app.Div().Class("tag-chips").Body(chips...)}
	if editing {
		body = append(body, app.Input().
			ID(tagInputID(t.ID)).
			Class("tag-input").
			Type("text").
			Placeholder("add tag…").
			OnInput(func(ctx app.Context, _ app.Event) { v.onTagInput(ctx, t.ID) }).
			OnKeyDown(func(ctx app.Context, e app.Event) { v.onTagKeyDown(ctx, e, t.ID) }).
			OnBlur(func(ctx app.Context, _ app.Event) { v.onTagBlur(ctx) }))
		if sugg := v.renderTagSuggestions(t); sugg != nil {
			body = append(body, sugg)
		}
	}

	// The whole cell is the click target that opens the editor; "editable"
	// styles the empty space as clickable when not already editing.
	cls := "tags-cell"
	if !editing {
		cls += " editable"
	}
	return app.Td().Class(cls).
		OnClick(func(ctx app.Context, e app.Event) { v.onTagsCellClick(ctx, t, e) }).
		Body(body...)
}

// renderTagSuggestions renders the typeahead dropdown for the active row, or nil
// when there is nothing to suggest. Picks fire on mousedown with preventDefault
// so the click is not swallowed by the input's blur (which would otherwise hide
// the dropdown first).
func (v *dashboardView) renderTagSuggestions(t transaction) app.UI {
	matches := filterTagSuggestions(v.allTags, v.tagDraft, t.Tags)
	if len(matches) == 0 {
		return nil
	}
	return app.Div().Class("tag-suggestions").Body(
		app.Range(matches).Slice(func(i int) app.UI {
			tag := matches[i]
			return app.Button().Type("button").Class("tag-suggestion").Text(tag).
				OnMouseDown(func(ctx app.Context, e app.Event) {
					e.PreventDefault()
					v.addTag(ctx, t.ID, tag)
				})
		}),
	)
}

func (v *dashboardView) pendingBadge(pending bool) app.UI {
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
