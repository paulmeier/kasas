package dashboard

import (
	"context"
	"sort"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// labelEditing is the inline label editor, factored out of the Dashboard so the
// Search page can reuse it verbatim. A view embeds it and, in OnMount, wires the
// three hooks below to its own transaction slice and error field. The render
// path (renderLabelsCell) reads only embedded state, so it works before the
// hooks are set — that keeps the dashboard's render tests hook-free.
//
// It deliberately holds no *apiClient field: both embedding views already
// promote chrome.client, and a second promoted client would make `v.client`
// ambiguous. The persistence call is injected via the setLabels hook instead.
type labelEditing struct {
	// txnByID returns a pointer to the transaction with the given id in the host
	// view's slice (or nil). It MUST return the addressable slice element
	// (&slice[i]) so optimistic label edits mutate the row in place.
	txnByID func(id string) *transaction
	// setLabels persists a transaction's full label set and returns the
	// server-normalized result. Wire to apiClient.setLabels.
	setLabels func(ctx context.Context, id string, labels map[string]string) (map[string]string, error)
	// reportError surfaces a save failure in the host view (e.g. sets errMsg).
	reportError func(msg string)

	// allLabels is the global label vocabulary as "key: value" strings (from
	// /api/v1/labels) that powers the typeahead. Only one row's add-label input is
	// active at a time: labelEditID is that row's transaction id ("" = none) and
	// labelDraft mirrors the input's text so suggestions can be filtered. The add
	// input is uncontrolled (its DOM value is the source of truth and is cleared
	// imperatively), so labelDraft is only used to decide what to suggest.
	allLabels   []string
	labelDraft  string
	labelEditID string
}

// loadVocab fetches the global label vocabulary for the typeahead. Best-effort:
// if it fails the suggestions are simply empty until the next successful load.
// The client is passed in rather than held, to avoid a promoted-field clash with
// chrome.client.
func (e *labelEditing) loadVocab(ctx app.Context, client *apiClient) {
	ctx.Async(func() {
		labels, err := client.labelSuggestions(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return
			}
			e.allLabels = labels
			ctx.Update()
		})
	})
}

// formatLabel renders a key/value pair as the "key: value" string shown on chips
// and in suggestions.
func formatLabel(key, value string) string { return key + ": " + value }

// parseLabel splits a "key: value" input into its trimmed parts. ok is false when
// there is no colon or either side is empty (labels are strictly key:value).
func parseLabel(raw string) (key, value string, ok bool) {
	i := strings.Index(raw, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(raw[:i])
	value = strings.TrimSpace(raw[i+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

// addLabel parses a typed/picked "key: value" and applies it to a transaction.
// Invalid input (no colon or an empty side) is rejected: nothing is saved and the
// input is left intact so the user can fix it. On a valid add the input is cleared
// and re-focused (via clearLabelInput) so the user can keep adding labels.
func (e *labelEditing) addLabel(ctx app.Context, id, raw string) {
	key, value, ok := parseLabel(raw)
	if !ok {
		return // leave the input as-is for the user to correct
	}
	if t := e.txnByID(id); t != nil {
		next := cloneLabels(t.Labels)
		next[key] = value // one value per key: a repeat key replaces its value
		e.saveLabels(ctx, id, next)
	}
	// Clear + refocus last, so the focus lands after the chip-insert re-render.
	e.clearLabelInput(ctx, id)
}

// cloneLabels returns a non-nil shallow copy of a labels map, so optimistic edits
// don't mutate the row's current map before the save is confirmed.
func cloneLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, val := range in {
		out[k] = val
	}
	return out
}

// removeLabel drops a key from a transaction's labels.
func (e *labelEditing) removeLabel(ctx app.Context, id, key string) {
	t := e.txnByID(id)
	if t == nil {
		return
	}
	next := cloneLabels(t.Labels)
	delete(next, key)
	e.saveLabels(ctx, id, next)
}

// saveLabels optimistically applies the new label set to the row, then persists
// it. On failure it reverts and surfaces the error; on success it adopts the
// server-normalized set and folds any new labels into the suggestion vocabulary.
func (e *labelEditing) saveLabels(ctx app.Context, id string, next map[string]string) {
	t := e.txnByID(id)
	if t == nil {
		return
	}
	prev := t.Labels
	t.Labels = next
	e.mergeVocab(next)
	ctx.Update()

	ctx.Async(func() {
		saved, err := e.setLabels(context.Background(), id, next)
		ctx.Dispatch(func(ctx app.Context) {
			cur := e.txnByID(id)
			if err != nil {
				if cur != nil {
					cur.Labels = prev // revert the optimistic update
				}
				if e.reportError != nil {
					e.reportError("Failed to save labels: " + err.Error())
				}
				ctx.Update()
				return
			}
			// Adopt the server-normalized set, but only re-render when it
			// actually differs from the optimistic one. Skipping the no-op render
			// avoids recreating the add-label input and stealing its focus while the
			// user is still labeling the row.
			if cur != nil && !equalLabels(cur.Labels, saved) {
				cur.Labels = saved
				e.mergeVocab(saved)
				ctx.Update()
				return
			}
			e.mergeVocab(saved)
		})
	})
}

// equalLabels reports whether two label maps are identical.
func equalLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if bv, ok := b[k]; !ok || av != bv {
			return false
		}
	}
	return true
}

// mergeVocab folds labels into allLabels (as "key: value" strings,
// case-insensitive), keeping it sorted, so freshly created labels are immediately
// suggestable without a refetch.
func (e *labelEditing) mergeVocab(labels map[string]string) {
	for key, value := range labels {
		s := formatLabel(key, value)
		found := false
		for _, existing := range e.allLabels {
			if strings.EqualFold(existing, s) {
				found = true
				break
			}
		}
		if !found {
			e.allLabels = append(e.allLabels, s)
		}
	}
	sort.Slice(e.allLabels, func(i, j int) bool {
		return strings.ToLower(e.allLabels[i]) < strings.ToLower(e.allLabels[j])
	})
}

// clearLabelInput resets the draft and clears the row's uncontrolled add-label
// input imperatively. go-app v10 drops empty value attributes, so a controlled
// Value("") would not clear the field — set the DOM value directly instead.
// After the re-render (which can recreate the input node and drop focus), it
// re-focuses the input so the user can keep adding labels and a later click-away
// still closes the editor via blur.
func (e *labelEditing) clearLabelInput(ctx app.Context, id string) {
	e.labelDraft = ""
	e.focusLabelInput(ctx, id, true)
	ctx.Update()
	// Re-assert focus after the re-render. With the chips in their own container
	// the input node is reused (so focus is usually preserved already), but this
	// keeps it robust if go-app recreates it.
	ctx.Defer(func(ctx app.Context) {
		if e.labelEditID == id {
			e.focusLabelInput(ctx, id, false)
		}
	})
}

// focusLabelInput focuses a row's add-label input by id, optionally clearing its
// value first. No-op when the element is absent (e.g. not currently editing).
func (e *labelEditing) focusLabelInput(_ app.Context, id string, clear bool) {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	el := doc.Call("getElementById", labelInputID(id))
	if !el.Truthy() {
		return
	}
	if clear {
		el.Set("value", "")
	}
	el.Call("focus")
}

// onLabelsCellClick opens the add-label editor for a row when its Labels cell is
// clicked in empty space, then focuses the freshly-rendered input. Clicks on the
// cell's interactive children (remove × buttons, the input, suggestion buttons)
// are ignored so they keep their own behavior.
func (e *labelEditing) onLabelsCellClick(ctx app.Context, t transaction, ev app.Event) {
	if target := ev.Get("target"); target.Truthy() {
		switch target.Get("tagName").String() {
		case "BUTTON", "INPUT":
			return
		}
	}
	if e.labelEditID == t.ID {
		return // already editing this row
	}
	e.labelEditID = t.ID
	e.labelDraft = ""
	ctx.Update()
	// Focus after the input has been rendered into the DOM.
	ctx.Defer(func(ctx app.Context) {
		if e.labelEditID == t.ID {
			e.focusLabelInput(ctx, t.ID, false)
		}
	})
}

func (e *labelEditing) onLabelInput(ctx app.Context, id string) {
	e.labelEditID = id
	e.labelDraft = ctx.JSSrc().Get("value").String()
	ctx.Update()
}

func (e *labelEditing) onLabelKeyDown(ctx app.Context, ev app.Event, id string) {
	switch ev.Get("key").String() {
	case "Enter":
		ev.PreventDefault()
		e.addLabel(ctx, id, ctx.JSSrc().Get("value").String())
	case "Escape":
		// Closing the editor removes the input, which drops focus.
		e.labelEditID = ""
		e.labelDraft = ""
		ctx.Update()
	}
}

// onLabelBlur hides the suggestions when the input loses focus. Suggestion picks
// use mousedown + preventDefault, which keeps the input focused, so blur does
// not fire on a pick and the click is not lost.
func (e *labelEditing) onLabelBlur(ctx app.Context) {
	e.labelEditID = ""
	e.labelDraft = ""
	ctx.Update()
}

// filterLabelSuggestions returns vocabulary labels ("key: value") matching the
// draft (case-insensitive substring), excluding those already applied to the row.
// The list is capped so the dropdown stays compact.
func filterLabelSuggestions(all []string, draft string, applied map[string]string) []string {
	const maxSuggestions = 8
	d := strings.ToLower(strings.TrimSpace(draft))
	if d == "" {
		return nil
	}
	isApplied := func(s string) bool {
		key, value, ok := parseLabel(s)
		if !ok {
			return false
		}
		cur, ok := applied[key]
		return ok && cur == value
	}
	var out []string
	for _, s := range all {
		if isApplied(s) {
			continue
		}
		if strings.Contains(strings.ToLower(s), d) {
			out = append(out, s)
			if len(out) >= maxSuggestions {
				break
			}
		}
	}
	return out
}

// labelInputID is the stable DOM id of a row's add-label input, used to clear it.
func labelInputID(txnID string) string { return "label-input-" + txnID }

// renderLabelsCell renders the Labels cell: each label as a "key: value" chip with
// a remove (×) button. The add-label input is hidden until the cell is clicked
// (see onLabelsCellClick) — so unlabeled rows are just empty, clickable space
// rather than a wall of input boxes. While editing, typing shows a typeahead
// dropdown.
func (e *labelEditing) renderLabelsCell(t transaction) app.UI {
	editing := e.labelEditID == t.ID

	keys := sortedLabelKeys(t.Labels)
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		key, value := k, t.Labels[k] // capture for the closure
		chips = append(chips, app.Span().Class("label-chip").Body(
			app.Span().Class("label-label").Text(formatLabel(key, value)),
			app.Button().Type("button").Class("label-remove").Title("Remove "+key).Text("×").
				OnClick(func(ctx app.Context, _ app.Event) { e.removeLabel(ctx, t.ID, key) }),
		))
	}

	// Chips live in their own container so adding/removing one never touches the
	// add-label input that follows it. Keeping the input at a stable DOM position
	// lets go-app reuse the node across re-renders, which preserves its focus
	// while the user keeps labeling the row.
	body := []app.UI{app.Div().Class("label-chips").Body(chips...)}
	if editing {
		body = append(body, app.Input().
			ID(labelInputID(t.ID)).
			Class("label-input").
			Type("text").
			Placeholder("key: value").
			OnInput(func(ctx app.Context, _ app.Event) { e.onLabelInput(ctx, t.ID) }).
			OnKeyDown(func(ctx app.Context, ev app.Event) { e.onLabelKeyDown(ctx, ev, t.ID) }).
			OnBlur(func(ctx app.Context, _ app.Event) { e.onLabelBlur(ctx) }))
		if sugg := e.renderLabelSuggestions(t); sugg != nil {
			body = append(body, sugg)
		}
	}

	// The whole cell is the click target that opens the editor; "editable"
	// styles the empty space as clickable when not already editing.
	cls := "labels-cell"
	if !editing {
		cls += " editable"
	}
	return app.Td().Class(cls).
		OnClick(func(ctx app.Context, ev app.Event) { e.onLabelsCellClick(ctx, t, ev) }).
		Body(body...)
}

// sortedLabelKeys returns a transaction's label keys in sorted order so chips
// render deterministically (Go map iteration is randomized).
func sortedLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderLabelSuggestions renders the typeahead dropdown for the active row, or nil
// when there is nothing to suggest. Picks fire on mousedown with preventDefault
// so the click is not swallowed by the input's blur (which would otherwise hide
// the dropdown first).
func (e *labelEditing) renderLabelSuggestions(t transaction) app.UI {
	matches := filterLabelSuggestions(e.allLabels, e.labelDraft, t.Labels)
	if len(matches) == 0 {
		return nil
	}
	return app.Div().Class("label-suggestions").Body(
		app.Range(matches).Slice(func(i int) app.UI {
			s := matches[i]
			return app.Button().Type("button").Class("label-suggestion").Text(s).
				OnMouseDown(func(ctx app.Context, ev app.Event) {
					ev.PreventDefault()
					e.addLabel(ctx, t.ID, s)
				})
		}),
	)
}
