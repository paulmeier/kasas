package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// accountsView is the Accounts page: a table of every known account (synced and
// manual) with its balance, alongside create/edit/delete of manual accounts. The
// account overview used to live on the Dashboard as a grid of cards; it is now a
// page of its own, rendered as a compact table.
type accountsView struct {
	app.Compo
	chrome         // shared sidebar + API client + version badge
	accountEditing // "Add/Edit account" modal (manual accounts only)

	accounts []account
	loading  bool
	errMsg   string
}

func (v *accountsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx) // wires v.client, sidebar state, version badge
	v.acctCreate = v.client.createAccount
	v.acctUpdate = v.client.updateAccount
	v.acctDelete = v.client.deleteAccount
	v.acctAfterChange = v.loadAccounts
	v.acctReportError = func(msg string) { v.errMsg = msg }
	v.loadAccounts(ctx)
}

func (v *accountsView) loadAccounts(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		accts, err := v.client.accounts(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.accounts = accts
			ctx.Update()
		})
	})
}

func (v *accountsView) Render() app.UI {
	return v.renderShell(navAccounts,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Accounts"),
			app.Span().Class("page-subtitle").Text("Your synced and manual accounts"),
		),
		v.renderControls(),
		v.renderError(),
		v.renderContent(),
		v.renderAccountEditor(),
	)
}

func (v *accountsView) renderControls() app.UI {
	return app.Div().Class("controls").Body(
		app.Span().Class("controls-spacer"),
		app.Button().Class("btn btn-primary add-acct-btn").Text("+ Add account").
			OnClick(func(ctx app.Context, _ app.Event) { v.openCreateAccount(ctx) }),
	)
}

func (v *accountsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *accountsView) renderContent() app.UI {
	if v.loading && len(v.accounts) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.accounts) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No accounts yet."),
			app.P().Class("empty-hint").
				Text("Connect SimpleFIN in Settings to sync accounts, or add a manual account above."),
		)
	}
	return app.Table().Class("txns accounts-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Account"),
				app.Th().Text("Source"),
				app.Th().Text("Currency"),
				app.Th().Class("right").Text("Balance"),
				app.Th().Text("Balance date"),
				app.Th().Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.accounts).Slice(func(i int) app.UI {
				return v.renderRow(v.accounts[i])
			}),
		),
	)
}

func (v *accountsView) renderRow(a account) app.UI {
	balanceClass := "amount pos"
	if strings.HasPrefix(strings.TrimSpace(a.Balance), "-") {
		balanceClass = "amount neg"
	}
	// Edit/Delete are offered only for manually-created accounts; synced accounts
	// are bridge-owned (their fields would be clobbered on the next sync).
	var actions []app.UI
	if a.Source == "manual" {
		actions = []app.UI{
			app.Button().Type("button").Class("row-edit-btn").Title("Edit account").
				OnClick(func(ctx app.Context, _ app.Event) { v.openEditAccount(ctx, a) }).
				Text("✎"),
			app.Button().Type("button").Class("row-delete-btn").Title("Delete account").
				OnClick(func(ctx app.Context, _ app.Event) { v.onDeleteAccount(ctx, a) }).
				Text("🗑"),
		}
	}
	return app.Tr().Body(
		app.Td().Class("acct-name").Text(a.Name),
		app.Td().Body(accountSourceBadge(a.Source)),
		app.Td().Text(a.Currency),
		app.Td().Class(balanceClass).Text(a.Balance),
		app.Td().Class("acct-balance-date").Text(formatBalanceDate(a.BalanceDate)),
		app.Td().Class("row-actions").Body(actions...),
	)
}

// accountSourceBadge renders the account's provenance as a small badge ("manual"
// or "simplefin"), matching the source distinction used elsewhere in the UI.
func accountSourceBadge(src string) app.UI {
	cls, label := "badge src-simplefin", src
	switch src {
	case "manual":
		cls = "badge src-manual"
	case "":
		label = "unknown"
	}
	return app.Span().Class(cls).Text(label)
}

// formatBalanceDate renders an account's balance date, or an em dash when unset.
func formatBalanceDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}
