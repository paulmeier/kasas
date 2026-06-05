package dashboard

import (
	"context"
	"fmt"
	"strconv"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// tagsView is the Tags page: a table of every tag in the vocabulary with the
// number of transactions carrying it, and a per-row delete that strips the tag
// from all of them. Tags are created on the Dashboard (the inline tag editor),
// so this page has no create control.
type tagsView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	tags    []tagCount
	loading bool
	errMsg  string
}

func (v *tagsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.fetchTags(ctx)
}

func (v *tagsView) fetchTags(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		tags, err := v.client.tagCounts(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.tags = tags
			ctx.Update()
		})
	})
}

// onDelete confirms (when the tag is in use) then strips the tag from every
// transaction. The row is removed optimistically and restored on failure.
func (v *tagsView) onDelete(ctx app.Context, t tagCount) {
	msg := fmt.Sprintf("Delete the tag %q?", t.Name)
	if t.Count > 0 {
		msg = fmt.Sprintf("Remove the tag %q from %s?", t.Name, plural(t.Count, "transaction"))
	}
	if !app.Window().Call("confirm", msg).Bool() {
		return
	}

	prev := v.tags
	v.tags = removeTagCount(v.tags, t.Name)
	ctx.Update()

	ctx.Async(func() {
		_, err := v.client.deleteTag(context.Background(), t.Name)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.tags = prev // revert the optimistic removal
				v.errMsg = "Failed to delete tag: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			ctx.Update()
		})
	})
}

// removeTagCount returns tags without the entry whose name matches (exact match:
// names come straight from the rendered list).
func removeTagCount(tags []tagCount, name string) []tagCount {
	out := make([]tagCount, 0, len(tags))
	for _, t := range tags {
		if t.Name != name {
			out = append(out, t)
		}
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

func (v *tagsView) Render() app.UI {
	return v.renderShell(navTags,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Tags"),
			app.Span().Class("page-subtitle").
				Text("Tags on your transactions. Add tags from the Dashboard's tag editor."),
		),
		v.renderError(),
		v.renderContent(),
	)
}

func (v *tagsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *tagsView) renderContent() app.UI {
	if v.loading && len(v.tags) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.tags) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No tags yet."),
			app.P().Class("empty-hint").
				Text("Open the Dashboard and add a tag to any transaction — it'll show up here."),
		)
	}
	return app.Table().Class("txns tags-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Tag"),
				app.Th().Class("right").Text("Transactions"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.tags).Slice(func(i int) app.UI {
				return v.renderRow(v.tags[i])
			}),
		),
	)
}

func (v *tagsView) renderRow(t tagCount) app.UI {
	return app.Tr().Body(
		app.Td().Body(
			app.Span().Class("tag-chip").Body(
				app.Span().Class("tag-label").Text(t.Name),
			),
		),
		app.Td().Class("right tag-count").Text(strconv.Itoa(t.Count)),
		app.Td().Class("right tag-actions").Body(
			app.Button().Type("button").Class("tag-delete").Title("Delete tag").
				OnClick(func(ctx app.Context, _ app.Event) { v.onDelete(ctx, t) }).
				Body(iconTrash()),
		),
	)
}
