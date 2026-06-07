package dashboard

import (
	"context"
	"sort"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// historyViewing is the per-transaction history modal, factored out so both the
// Dashboard and the Search page can reuse it. A view embeds it, wires the
// fetchHistory hook in OnMount, renders a per-row clock button (renderHistoryButton)
// and renders the modal once (renderHistoryModal). The render path reads only
// embedded state, so it works before the hook is set — keeping the views'
// PrintHTML tests hook-free.
//
// Like labelEditing it holds no *apiClient field (both views already promote
// chrome.client; a second would make v.client ambiguous); the fetch is injected.
type historyViewing struct {
	// fetchHistory loads a transaction's version history. Wire to
	// apiClient.transactionHistory.
	fetchHistory func(ctx context.Context, id string) ([]version, error)

	historyOpen     bool      // the modal is showing
	historyTxnID    string    // transaction whose history is loaded/loading
	historyTitle    string    // the row's description, shown in the modal header
	historyVersions []version // loaded versions (oldest first; rendered newest first)
	historyLoading  bool
	historyErr      string
}

// openHistory shows the modal for a transaction and fetches its history. A response
// for a transaction other than the currently-open one is dropped (the user moved on).
func (h *historyViewing) openHistory(ctx app.Context, t transaction) {
	h.historyOpen = true
	h.historyTxnID = t.ID
	h.historyTitle = displayDesc(t)
	h.historyVersions = nil
	h.historyErr = ""
	h.historyLoading = true
	ctx.Update()

	if h.fetchHistory == nil {
		return
	}
	id := t.ID
	ctx.Async(func() {
		vers, err := h.fetchHistory(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if !h.historyOpen || h.historyTxnID != id {
				return
			}
			h.historyLoading = false
			if err != nil {
				h.historyErr = err.Error()
				ctx.Update()
				return
			}
			h.historyVersions = vers
			ctx.Update()
		})
	})
}

func (h *historyViewing) closeHistory(ctx app.Context) {
	h.historyOpen = false
	h.historyTxnID = ""
	h.historyVersions = nil
	h.historyErr = ""
	h.historyLoading = false
	ctx.Update()
}

// renderHistoryButton is the per-row clock control that opens the history modal. It
// is always in the DOM but styled to reveal on row hover/focus (see the CSS), so the
// table stays uncluttered.
func (h *historyViewing) renderHistoryButton(t transaction) app.UI {
	return app.Button().Type("button").Class("history-btn").Title("View history").
		OnClick(func(ctx app.Context, _ app.Event) { h.openHistory(ctx, t) }).
		Body(iconHistory())
}

// renderHistoryModal renders the overlay + modal, or nothing when closed. It reuses
// the shared modal chrome (the same classes as the search-syntax help modal): the
// backdrop closes it; clicks inside are stopped so they don't bubble out.
func (h *historyViewing) renderHistoryModal() app.UI {
	if !h.historyOpen {
		return app.Text("")
	}
	onClose := func(ctx app.Context, _ app.Event) { h.closeHistory(ctx) }
	return app.Div().Class("modal-overlay").OnClick(onClose).Body(
		app.Div().Class("modal history-modal").
			OnClick(func(_ app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.Div().Class("history-titles").Body(
						app.H2().Class("modal-title").Text("History"),
						app.Span().Class("history-sub").Text(h.historyTitle),
					),
					app.Button().Type("button").Class("modal-close").Title("Close").Text("×").OnClick(onClose),
				),
				app.Div().Class("modal-body").Body(h.renderHistoryContent()),
			),
	)
}

func (h *historyViewing) renderHistoryContent() app.UI {
	switch {
	case h.historyLoading:
		return app.Div().Class("status").Text("Loading…")
	case h.historyErr != "":
		return app.Div().Class("error").Text("Error: " + h.historyErr)
	case len(h.historyVersions) == 0:
		return app.Div().Class("status").Text("No history yet. This transaction hasn't changed since history began.")
	}
	return app.Div().Class("history-timeline").Body(
		app.Range(h.historyVersions).Slice(func(i int) app.UI {
			// Render newest first (the slice is oldest-first).
			return renderVersion(h.historyVersions[len(h.historyVersions)-1-i])
		}),
	)
}

func renderVersion(ver version) app.UI {
	return app.Div().Class("history-version").Body(
		app.Div().Class("history-version-head").Body(
			app.Span().Class("history-vnum").Text("v"+strconv.Itoa(ver.Version)),
			app.Span().Class("badge chg-kind "+changeKindClass(ver.ChangeKind)).Text(ver.ChangeKind),
			app.Span().Class("history-vtime").Text(ver.OccurredAt.Local().Format("2006-01-02 15:04:05")),
		),
		renderVersionDiff(ver.Diff),
		app.Details().Class("history-snapshot").Body(
			app.Summary().Text("snapshot"),
			app.Pre().Class("evt-json").Text(prettyJSON(ver.Transaction)),
		),
	)
}

// renderVersionDiff renders the change from the previous version: scalar field
// changes first (in the API's fixed order), then label additions, changes, and
// removals (each alphabetical). The first version's diff is a "birth" diff (every
// set field as a change from empty).
func renderVersionDiff(d versionDiff) app.UI {
	var rows []app.UI
	for _, f := range d.Fields {
		rows = append(rows, diffChangeRow(f.Field, f.From, f.To))
	}
	for _, k := range sortedDiffKeys(d.LabelsAdded) {
		rows = append(rows, diffLabelRow("diff-add", "+", "label "+k, d.LabelsAdded[k], "diff-new"))
	}
	for _, k := range sortedDiffKeys(d.LabelsChanged) {
		c := d.LabelsChanged[k]
		rows = append(rows, diffChangeRow("label "+k, c.From, c.To))
	}
	for _, k := range sortedDiffKeys(d.LabelsRemoved) {
		rows = append(rows, diffLabelRow("diff-remove", "−", "label "+k, d.LabelsRemoved[k], "diff-old"))
	}
	if len(rows) == 0 {
		return app.Div().Class("history-diff empty").Text("No field changes")
	}
	return app.Div().Class("history-diff").Body(rows...)
}

// diffChangeRow renders a "field: from → to" line (used for scalar fields and for
// labels whose value changed). An empty side renders as a muted placeholder.
func diffChangeRow(field, from, to string) app.UI {
	return app.Div().Class("diff-row diff-change").Body(
		app.Span().Class("diff-field").Text(field),
		diffVal(from, "diff-old"),
		app.Span().Class("diff-arrow").Text("→"),
		diffVal(to, "diff-new"),
	)
}

// diffLabelRow renders an added/removed label line with a +/− sign.
func diffLabelRow(rowClass, sign, field, value, valClass string) app.UI {
	return app.Div().Class("diff-row "+rowClass).Body(
		app.Span().Class("diff-sign").Text(sign),
		app.Span().Class("diff-field").Text(field),
		app.Span().Class(valClass).Text(value),
	)
}

func diffVal(s, cls string) app.UI {
	if s == "" {
		return app.Span().Class("diff-empty").Text("(none)")
	}
	return app.Span().Class(cls).Text(s)
}

// changeKindClass colour-codes a version by its change kind.
func changeKindClass(kind string) string {
	switch kind {
	case "imported":
		return "chg-imported"
	case "synced":
		return "chg-synced"
	case "labeled":
		return "chg-labeled"
	case "extended":
		return "chg-extended"
	default:
		return ""
	}
}

// sortedDiffKeys returns a diff map's keys in sorted order so rows render
// deterministically (Go map iteration is randomized).
func sortedDiffKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
