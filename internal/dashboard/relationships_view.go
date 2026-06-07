package dashboard

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/paulmeier/kasas/internal/relationships"
)

// relationshipsViewing is the per-transaction relationships modal, factored out so
// both the Dashboard and the Search page reuse it. A view embeds it, wires the
// hooks below in OnMount, renders a per-row link button (renderRelationshipsButton)
// and renders the modal once (renderRelationshipsModal).
//
// Unlike the read-only history/provenance modals, this one EDITS: it lists a
// transaction's inbound + outbound edges and lets the user assert a new outbound
// edge (pick a target transaction via a typeahead, give it a kind) or remove one.
// Like the other shared mixins it holds no *apiClient field (the views already
// promote chrome.client); the calls are injected as hooks, and the render path
// reads only embedded state so it works before the hooks are set (keeping the
// views' PrintHTML tests hook-free).
type relationshipsViewing struct {
	// fetchRelationships loads a transaction's full neighborhood (inbound+outbound).
	// Wire to apiClient.transactionRelationships.
	fetchRelationships func(ctx context.Context, id string) ([]relationshipEdge, error)
	// createRelationship asserts an outbound edge id --kind--> target. Wire to
	// apiClient.createTransactionRelationship.
	createRelationship func(ctx context.Context, id, kind, target string) error
	// deleteRelationship removes the edge ownerID --kind--> target. Wire to
	// apiClient.deleteTransactionRelationship.
	deleteRelationship func(ctx context.Context, ownerID, kind, target string) error
	// relAllTxns returns every loaded transaction, for the target typeahead and to
	// render a related transaction's description/amount instead of a bare id.
	relAllTxns func() []transaction
	// relTxnByID returns the addressable row for a transaction (or nil), so a
	// successful edit can refresh that row's outbound indicator in place.
	relTxnByID func(id string) *transaction
	// relReportError surfaces a load/save failure in the host view.
	relReportError func(msg string)

	relOpen    bool               // the modal is showing
	relTxnID   string             // focal transaction whose neighborhood is shown
	relTitle   string             // the row's description, shown in the modal header
	relEdges   []relationshipEdge // loaded neighborhood (outbound + inbound)
	relLoading bool
	relErr     string // load error (distinct from the add-form error)

	// Add-form state. The kind input is free text; the target input is a typeahead
	// over loaded transactions. relTargetID is the picked target's id ("" until a
	// suggestion is chosen, which is what gates the Add button). Both inputs are
	// uncontrolled (their DOM value is the source of truth, set imperatively on
	// pick), so the drafts only drive suggestion filtering and the enabled state.
	relKindDraft   string
	relTargetDraft string
	relTargetID    string
	relAddErr      string
	relSubmitting  bool
}

const (
	relKindInputID   = "rel-kind-input"
	relTargetInputID = "rel-target-input"
)

// openRelationships shows the modal for a transaction and fetches its neighborhood.
// A response for a transaction other than the currently-open one is dropped.
func (v *relationshipsViewing) openRelationships(ctx app.Context, t transaction) {
	v.relOpen = true
	v.relTxnID = t.ID
	v.relTitle = displayDesc(t)
	v.relEdges = nil
	v.relErr = ""
	v.relLoading = true
	v.resetRelForm()
	ctx.Update()
	v.loadRelationships(ctx, t.ID)
}

func (v *relationshipsViewing) loadRelationships(ctx app.Context, id string) {
	if v.fetchRelationships == nil {
		return
	}
	ctx.Async(func() {
		edges, err := v.fetchRelationships(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if !v.relOpen || v.relTxnID != id {
				return
			}
			v.relLoading = false
			if err != nil {
				v.relErr = err.Error()
				ctx.Update()
				return
			}
			v.relEdges = edges
			v.syncRowOutbound(id, edges)
			ctx.Update()
		})
	})
}

// syncRowOutbound refreshes the focal row's own outbound edges from a freshly
// loaded neighborhood, so the row's relationship indicator stays in step with edits
// made in the modal without a full table reload.
func (v *relationshipsViewing) syncRowOutbound(id string, edges []relationshipEdge) {
	if v.relTxnByID == nil {
		return
	}
	t := v.relTxnByID(id)
	if t == nil {
		return
	}
	out := make([]relationships.Relationship, 0, len(edges))
	for _, e := range edges {
		if e.Direction == relationships.DirectionOutbound {
			out = append(out, relationships.Relationship{Kind: e.Kind, Target: e.OtherTransactionID})
		}
	}
	t.Relationships = out
}

func (v *relationshipsViewing) closeRelationships(ctx app.Context) {
	v.relOpen = false
	v.relTxnID = ""
	v.relEdges = nil
	v.relErr = ""
	v.relLoading = false
	v.resetRelForm()
	ctx.Update()
}

func (v *relationshipsViewing) resetRelForm() {
	v.relKindDraft = ""
	v.relTargetDraft = ""
	v.relTargetID = ""
	v.relAddErr = ""
	v.relSubmitting = false
}

// renderRelationshipsButton is the per-row link control that opens the modal. It is
// always in the DOM but revealed on row hover/focus (see CSS); a small count badge
// shows when the row has outbound edges.
func (v *relationshipsViewing) renderRelationshipsButton(t transaction) app.UI {
	n := len(t.Relationships)
	title := "View relationships"
	body := []app.UI{iconRelationships()}
	if n > 0 {
		title = "View relationships (" + strconv.Itoa(n) + ")"
		body = append(body, app.Span().Class("rel-count").Text(strconv.Itoa(n)))
	}
	return app.Button().Type("button").Class("relationships-btn").Title(title).
		OnClick(func(ctx app.Context, _ app.Event) { v.openRelationships(ctx, t) }).
		Body(body...)
}

// renderRelationshipsModal renders the overlay + modal, or nothing when closed,
// reusing the shared modal chrome (backdrop closes it; inner clicks are stopped).
func (v *relationshipsViewing) renderRelationshipsModal() app.UI {
	if !v.relOpen {
		return app.Text("")
	}
	onClose := func(ctx app.Context, _ app.Event) { v.closeRelationships(ctx) }
	return app.Div().Class("modal-overlay").OnClick(onClose).Body(
		app.Div().Class("modal relationships-modal").
			OnClick(func(_ app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.Div().Class("history-titles").Body(
						app.H2().Class("modal-title").Text("Relationships"),
						app.Span().Class("history-sub").Text(v.relTitle),
					),
					app.Button().Type("button").Class("modal-close").Title("Close").Text("×").OnClick(onClose),
				),
				app.Div().Class("modal-body").Body(v.renderRelationshipsContent()),
			),
	)
}

func (v *relationshipsViewing) renderRelationshipsContent() app.UI {
	switch {
	case v.relLoading:
		return app.Div().Class("status").Text("Loading…")
	case v.relErr != "":
		return app.Div().Class("error").Text("Error: " + v.relErr)
	}

	var outbound, inbound []relationshipEdge
	for _, e := range v.relEdges {
		if e.Direction == relationships.DirectionInbound {
			inbound = append(inbound, e)
		} else {
			outbound = append(outbound, e)
		}
	}

	return app.Div().Class("rel-sections").Body(
		v.renderEdgeSection("This transaction relates to", outbound, relationships.DirectionOutbound),
		v.renderEdgeSection("Related to by", inbound, relationships.DirectionInbound),
		v.renderAddForm(),
	)
}

// renderEdgeSection renders one direction's edges. Each row shows the kind and the
// other transaction (its description/amount when loaded, else its bare id) with a
// remove button. Removing an inbound edge edits the OTHER transaction (it owns that
// edge), which the handler accounts for.
func (v *relationshipsViewing) renderEdgeSection(heading string, edges []relationshipEdge, dir string) app.UI {
	rows := make([]app.UI, 0, len(edges))
	for _, e := range edges {
		edge := e // capture
		rows = append(rows, app.Div().Class("rel-edge").Body(
			app.Span().Class("badge rel-kind").Text(edge.Kind),
			app.Span().Class("rel-arrow").Text(arrowFor(dir)),
			app.Span().Class("rel-target").Text(v.describeTarget(edge.OtherTransactionID)),
			app.Button().Type("button").Class("label-remove rel-remove").Title("Remove").Text("×").
				OnClick(func(ctx app.Context, _ app.Event) { v.removeEdge(ctx, edge, dir) }),
		))
	}
	body := []app.UI{app.Div().Class("rel-section-head").Text(heading)}
	if len(rows) == 0 {
		body = append(body, app.Div().Class("rel-empty").Text("None"))
	} else {
		body = append(body, rows...)
	}
	return app.Div().Class("rel-section").Body(body...)
}

// renderAddForm renders the "add an outbound edge" form: a kind input (with quick
// chips for kinds already in use), a target typeahead over loaded transactions, and
// an Add button enabled once both a kind and a target are chosen.
func (v *relationshipsViewing) renderAddForm() app.UI {
	canAdd := relationships.NormalizeKind(v.relKindDraft) != "" && v.relTargetID != "" && !v.relSubmitting

	body := []app.UI{
		app.Div().Class("rel-section-head").Text("Add relationship"),
		app.Div().Class("rel-add-row").Body(
			app.Input().ID(relKindInputID).Class("rel-kind-field").Type("text").
				Placeholder("kind, e.g. refund_of").
				OnInput(func(ctx app.Context, _ app.Event) { v.onRelKindInput(ctx) }),
			app.Div().Class("rel-target-wrap").Body(
				app.Input().ID(relTargetInputID).Class("rel-target-field").Type("text").
					Placeholder("find target transaction…").
					OnInput(func(ctx app.Context, _ app.Event) { v.onRelTargetInput(ctx) }),
				v.renderTargetSuggestions(),
			),
			app.Button().Type("button").Class("btn-primary rel-add-btn").
				Disabled(!canAdd).Text("Add").
				OnClick(func(ctx app.Context, _ app.Event) { v.submitAdd(ctx) }),
		),
	}
	if chips := v.renderKindChips(); chips != nil {
		body = append(body, chips)
	}
	if v.relAddErr != "" {
		body = append(body, app.Div().Class("error rel-add-err").Text(v.relAddErr))
	}
	return app.Div().Class("rel-section rel-add").Body(body...)
}

// renderKindChips offers the relationship kinds already in use (across loaded
// transactions) as one-click fills for the kind field.
func (v *relationshipsViewing) renderKindChips() app.UI {
	kinds := v.knownKinds()
	if len(kinds) == 0 {
		return nil
	}
	return app.Div().Class("rel-kind-chips").Body(
		app.Range(kinds).Slice(func(i int) app.UI {
			k := kinds[i]
			return app.Button().Type("button").Class("rel-kind-chip").Text(k).
				OnClick(func(ctx app.Context, _ app.Event) {
					v.relKindDraft = k
					setElementValue(relKindInputID, k)
					ctx.Update()
				})
		}),
	)
}

func (v *relationshipsViewing) renderTargetSuggestions() app.UI {
	matches := v.filterTargets()
	if len(matches) == 0 {
		return nil
	}
	return app.Div().Class("label-suggestions rel-suggestions").Body(
		app.Range(matches).Slice(func(i int) app.UI {
			t := matches[i]
			return app.Button().Type("button").Class("label-suggestion").Text(relTxnSummary(t)).
				OnMouseDown(func(ctx app.Context, ev app.Event) {
					ev.PreventDefault()
					v.pickTarget(ctx, t)
				})
		}),
	)
}

func (v *relationshipsViewing) onRelKindInput(ctx app.Context) {
	v.relKindDraft = ctx.JSSrc().Get("value").String()
	ctx.Update()
}

// onRelTargetInput mirrors the target draft and clears any prior pick, so editing
// the text after choosing reopens the search.
func (v *relationshipsViewing) onRelTargetInput(ctx app.Context) {
	v.relTargetDraft = ctx.JSSrc().Get("value").String()
	v.relTargetID = ""
	ctx.Update()
}

// pickTarget selects a target transaction from the typeahead, filling the input
// imperatively (an uncontrolled field) and recording its id.
func (v *relationshipsViewing) pickTarget(ctx app.Context, t transaction) {
	v.relTargetID = t.ID
	v.relTargetDraft = relTxnSummary(t)
	setElementValue(relTargetInputID, v.relTargetDraft)
	ctx.Update()
}

func (v *relationshipsViewing) submitAdd(ctx app.Context) {
	kind := relationships.NormalizeKind(v.relKindDraft)
	target := v.relTargetID
	if kind == "" || target == "" || v.createRelationship == nil {
		return
	}
	id := v.relTxnID
	v.relSubmitting = true
	v.relAddErr = ""
	ctx.Update()
	ctx.Async(func() {
		err := v.createRelationship(context.Background(), id, kind, target)
		ctx.Dispatch(func(ctx app.Context) {
			v.relSubmitting = false
			if !v.relOpen || v.relTxnID != id {
				return
			}
			if err != nil {
				v.relAddErr = err.Error()
				ctx.Update()
				return
			}
			// Clear the form (including the uncontrolled inputs) and reload.
			v.resetRelForm()
			setElementValue(relKindInputID, "")
			setElementValue(relTargetInputID, "")
			v.relLoading = true
			ctx.Update()
			v.loadRelationships(ctx, id)
		})
	})
}

// removeEdge deletes one edge. An outbound edge is owned by the focal transaction;
// an inbound edge is owned by the other transaction, so the delete targets that one.
func (v *relationshipsViewing) removeEdge(ctx app.Context, e relationshipEdge, dir string) {
	if v.deleteRelationship == nil {
		return
	}
	owner, target := v.relTxnID, e.OtherTransactionID
	if dir == relationships.DirectionInbound {
		owner, target = e.OtherTransactionID, v.relTxnID
	}
	focal := v.relTxnID
	kind := e.Kind
	ctx.Async(func() {
		err := v.deleteRelationship(context.Background(), owner, kind, target)
		ctx.Dispatch(func(ctx app.Context) {
			if !v.relOpen || v.relTxnID != focal {
				return
			}
			if err != nil {
				if v.relReportError != nil {
					v.relReportError("Failed to remove relationship: " + err.Error())
				}
				v.relErr = err.Error()
				ctx.Update()
				return
			}
			v.relLoading = true
			ctx.Update()
			v.loadRelationships(ctx, focal)
		})
	})
}

// describeTarget renders a related transaction by its description/amount when it is
// loaded, falling back to its raw id (e.g. it is outside the current account
// filter).
func (v *relationshipsViewing) describeTarget(id string) string {
	if t, ok := v.lookupTxn(id); ok {
		return relTxnSummary(t)
	}
	return id
}

func (v *relationshipsViewing) lookupTxn(id string) (transaction, bool) {
	if v.relAllTxns == nil {
		return transaction{}, false
	}
	for _, t := range v.relAllTxns() {
		if t.ID == id {
			return t, true
		}
	}
	return transaction{}, false
}

// filterTargets returns up to 8 loaded transactions matching the target draft
// (case-insensitive over description/payee/amount/id), excluding the focal
// transaction and targets it already links to.
func (v *relationshipsViewing) filterTargets() []transaction {
	const maxSuggestions = 8
	if v.relAllTxns == nil || v.relTargetID != "" {
		return nil
	}
	d := strings.ToLower(strings.TrimSpace(v.relTargetDraft))
	if d == "" {
		return nil
	}
	existing := make(map[string]bool)
	for _, e := range v.relEdges {
		if e.Direction == relationships.DirectionOutbound {
			existing[e.OtherTransactionID] = true
		}
	}
	var out []transaction
	for _, t := range v.relAllTxns() {
		if t.ID == v.relTxnID || existing[t.ID] {
			continue
		}
		hay := strings.ToLower(t.Description + " " + t.Payee + " " + t.Amount + " " + t.ID)
		if strings.Contains(hay, d) {
			out = append(out, t)
			if len(out) >= maxSuggestions {
				break
			}
		}
	}
	return out
}

// knownKinds collects the distinct relationship kinds in use across loaded
// transactions' outbound edges, sorted, for the quick-pick chips.
func (v *relationshipsViewing) knownKinds() []string {
	if v.relAllTxns == nil {
		return nil
	}
	seen := make(map[string]bool)
	for _, t := range v.relAllTxns() {
		for _, e := range t.Relationships {
			seen[e.Kind] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// relTxnSummary renders a transaction compactly for the target picker and edge rows.
func relTxnSummary(t transaction) string {
	return t.Date.Format("2006-01-02") + " · " + displayDesc(t) + " · " + t.Amount
}

// arrowFor points away from the focal transaction for outbound edges and toward it
// for inbound ones.
func arrowFor(dir string) string {
	if dir == relationships.DirectionInbound {
		return "←"
	}
	return "→"
}
