package dashboard

import (
	"context"
	"fmt"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// labelsView is the Labels page: a table of every label (key:value pair) in the
// vocabulary with the number of transactions carrying it, and a per-row delete
// that strips that pair from all of them. Labels are created on the Dashboard
// (the inline label editor), so this page has no create control.
type labelsView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	labels  []labelCount
	loading bool
	errMsg  string
}

func (v *labelsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.fetchLabels(ctx)
}

func (v *labelsView) fetchLabels(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		labels, err := v.client.labelCounts(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.labels = labels
			ctx.Update()
		})
	})
}

// onDelete confirms (when the label is in use) then strips the key:value pair
// from every transaction. The row is removed optimistically and restored on
// failure.
func (v *labelsView) onDelete(ctx app.Context, l labelCount) {
	label := formatLabel(l.Key, l.Value)
	msg := fmt.Sprintf("Delete the label %q?", label)
	if l.Count > 0 {
		msg = fmt.Sprintf("Remove the label %q from %s?", label, plural(l.Count, "transaction"))
	}
	if !app.Window().Call("confirm", msg).Bool() {
		return
	}

	prev := v.labels
	v.labels = removeLabelCount(v.labels, l)
	ctx.Update()

	ctx.Async(func() {
		_, err := v.client.deleteLabel(context.Background(), l.Key, l.Value)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.labels = prev // revert the optimistic removal
				v.errMsg = "Failed to delete label: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			ctx.Update()
		})
	})
}

// removeLabelCount returns labels without the entry matching l's key and value
// (exact match: they come straight from the rendered list).
func removeLabelCount(labels []labelCount, l labelCount) []labelCount {
	out := make([]labelCount, 0, len(labels))
	for _, x := range labels {
		if x.Key == l.Key && x.Value == l.Value {
			continue
		}
		out = append(out, x)
	}
	return out
}

// plural renders "1 transaction" / "3 transactions".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (v *labelsView) Render() app.UI {
	return v.renderShell(navLabels,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Labels"),
			app.Span().Class("page-subtitle").
				Text("Key:value labels on your transactions. Add labels from the Dashboard's label editor."),
		),
		v.renderError(),
		v.renderContent(),
	)
}

func (v *labelsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *labelsView) renderContent() app.UI {
	if v.loading && len(v.labels) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.labels) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No labels yet."),
			app.P().Class("empty-hint").
				Text("Open the Dashboard and add a label (e.g. category: food) to any transaction — it'll show up here."),
		)
	}
	return app.Table().Class("txns labels-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Label"),
				app.Th().Class("right").Text("Transactions"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.labels).Slice(func(i int) app.UI {
				return v.renderRow(v.labels[i])
			}),
		),
	)
}

func (v *labelsView) renderRow(l labelCount) app.UI {
	return app.Tr().Body(
		app.Td().Body(
			app.Span().Class("label-chip").Body(
				app.Span().Class("label-label").Text(formatLabel(l.Key, l.Value)),
			),
		),
		app.Td().Class("right label-count").Text(strconv.Itoa(l.Count)),
		app.Td().Class("right label-actions").Body(
			app.Button().Type("button").Class("label-delete").Title("Delete label").
				OnClick(func(ctx app.Context, _ app.Event) { v.onDelete(ctx, l) }).
				Body(iconTrash()),
		),
	)
}
