package dashboard

import (
	"context"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// provenanceViewing is the per-transaction provenance modal, factored out like
// historyViewing so both the Dashboard and the Search page reuse it. A view embeds
// it, wires the fetchProvenance hook in OnMount, renders a per-row button
// (renderProvenanceButton) and renders the modal once (renderProvenanceModal). The
// render path reads only embedded state, so it works before the hook is set —
// keeping the views' PrintHTML tests hook-free.
//
// Where historyViewing shows full snapshots + diffs, this shows the origin summary:
// where the transaction came from and the ordered transformations that shaped it.
type provenanceViewing struct {
	// fetchProvenance loads a transaction's provenance. Wire to
	// apiClient.transactionProvenance.
	fetchProvenance func(ctx context.Context, id string) (provenance, error)

	provOpen    bool       // the modal is showing
	provTxnID   string     // transaction whose provenance is loaded/loading
	provTitle   string     // the row's description, shown in the modal header
	provData    provenance // loaded provenance
	provLoading bool
	provErr     string
}

// openProvenance shows the modal for a transaction and fetches its provenance. A
// response for a transaction other than the currently-open one is dropped.
func (h *provenanceViewing) openProvenance(ctx app.Context, t transaction) {
	h.provOpen = true
	h.provTxnID = t.ID
	h.provTitle = displayDesc(t)
	h.provData = provenance{}
	h.provErr = ""
	h.provLoading = true
	ctx.Update()

	if h.fetchProvenance == nil {
		return
	}
	id := t.ID
	ctx.Async(func() {
		p, err := h.fetchProvenance(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if !h.provOpen || h.provTxnID != id {
				return
			}
			h.provLoading = false
			if err != nil {
				h.provErr = err.Error()
				ctx.Update()
				return
			}
			h.provData = p
			ctx.Update()
		})
	})
}

func (h *provenanceViewing) closeProvenance(ctx app.Context) {
	h.provOpen = false
	h.provTxnID = ""
	h.provData = provenance{}
	h.provErr = ""
	h.provLoading = false
	ctx.Update()
}

// renderProvenanceButton is the per-row control that opens the provenance modal. Like
// the history clock it is always in the DOM but styled to reveal on row hover/focus.
func (h *provenanceViewing) renderProvenanceButton(t transaction) app.UI {
	return app.Button().Type("button").Class("provenance-btn").Title("View provenance").
		OnClick(func(ctx app.Context, _ app.Event) { h.openProvenance(ctx, t) }).
		Body(iconProvenance())
}

// renderProvenanceModal renders the overlay + modal, or nothing when closed. It reuses
// the shared modal chrome (same classes as the history modal).
func (h *provenanceViewing) renderProvenanceModal() app.UI {
	if !h.provOpen {
		return app.Text("")
	}
	onClose := func(ctx app.Context, _ app.Event) { h.closeProvenance(ctx) }
	return app.Div().Class("modal-overlay").OnClick(onClose).Body(
		app.Div().Class("modal provenance-modal").
			OnClick(func(_ app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.Div().Class("history-titles").Body(
						app.H2().Class("modal-title").Text("Provenance"),
						app.Span().Class("history-sub").Text(h.provTitle),
					),
					app.Button().Type("button").Class("modal-close").Title("Close").Text("×").OnClick(onClose),
				),
				app.Div().Class("modal-body").Body(h.renderProvenanceContent()),
			),
	)
}

func (h *provenanceViewing) renderProvenanceContent() app.UI {
	switch {
	case h.provLoading:
		return app.Div().Class("status").Text("Loading…")
	case h.provErr != "":
		return app.Div().Class("error").Text("Error: " + h.provErr)
	}
	p := h.provData
	return app.Div().Class("provenance").Body(
		// Origin: where the transaction came from.
		app.Div().Class("prov-origin").Body(
			provRow("Source", p.Source),
			provRow("Source ID", p.SourceTransactionID),
			provRow("Account", p.AccountID),
			provRow("Institution", p.Institution),
			provRow("Imported", formatLocalTime(p.ImportedAt)),
			provRow("Last seen", formatLocalTime(p.LastSeen)),
		),
		// Lineage: the ordered transformations, oldest first (the import, then on).
		app.H3().Class("prov-section").Text("Transformations"),
		h.renderTransformations(p.Transformations),
	)
}

func (h *provenanceViewing) renderTransformations(trs []transformation) app.UI {
	if len(trs) == 0 {
		return app.Div().Class("status").Text("No recorded transformations yet.")
	}
	return app.Div().Class("prov-transforms").Body(
		app.Range(trs).Slice(func(i int) app.UI {
			tr := trs[i]
			return app.Div().Class("prov-transform").Body(
				app.Div().Class("prov-transform-head").Body(
					app.Span().Class("badge chg-kind "+changeKindClass(tr.Kind)).Text(tr.Kind),
					app.Span().Class("prov-ttime").Text(formatLocalTime(tr.OccurredAt)),
				),
				app.Span().Class("prov-tsummary").Text(tr.Summary),
			)
		}),
	)
}

// provRow renders one labelled origin field, or nothing when the value is empty
// (e.g. an unresolved institution).
func provRow(label, value string) app.UI {
	if value == "" {
		return app.Text("")
	}
	return app.Div().Class("prov-row").Body(
		app.Span().Class("prov-label").Text(label),
		app.Span().Class("prov-value").Text(value),
	)
}

// formatLocalTime renders a timestamp in the viewer's local zone, or "" when unset
// (so an absent value is simply omitted from the origin block).
func formatLocalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
