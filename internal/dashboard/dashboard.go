// Package dashboard is a read-only, browser-side (WebAssembly) UI for browsing
// synced kasas accounts and transactions, built with go-app. It fetches data
// from the same-origin REST API and is served by Handler.
package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

const pageSize = 50

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
	txns     []transaction

	selectedAccount string // "" means all accounts
	offset          int
	loading         bool
	loadingMore     bool
	hasMore         bool
	errMsg          string

	// Update banner state.
	update    updateStatus
	updating  bool
	updateMsg string // post-apply message (covers the restart window)
	updateErr string
}

func (v *dashboardView) OnMount(ctx app.Context) {
	v.client = newAPIClient(originURL())
	v.loadAccounts(ctx)
	v.reloadTransactions(ctx)
	v.loadUpdateStatus(ctx)
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
	v.offset = 0
	v.txns = nil
	v.loading = true
	v.fetchTransactions(ctx, false)
}

func (v *dashboardView) fetchTransactions(ctx app.Context, more bool) {
	acctID := v.selectedAccount
	offset := v.offset
	ctx.Async(func() {
		txns, err := v.client.transactions(context.Background(), acctID, pageSize, offset)
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			v.loadingMore = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			if more {
				v.txns = append(v.txns, txns...)
			} else {
				v.txns = txns
			}
			v.offset += len(txns)
			v.hasMore = len(txns) == pageSize
			ctx.Update()
		})
	})
}

func (v *dashboardView) onAccountChange(ctx app.Context, _ app.Event) {
	v.selectedAccount = ctx.JSSrc().Get("value").String()
	v.reloadTransactions(ctx)
}

func (v *dashboardView) onLoadMore(ctx app.Context, _ app.Event) {
	if v.loadingMore || !v.hasMore {
		return
	}
	v.loadingMore = true
	v.fetchTransactions(ctx, true)
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
		v.renderLoadMore(),
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
			app.Option().Value("").Text("All accounts").Selected(v.selectedAccount == ""),
			app.Range(v.accounts).Slice(func(i int) app.UI {
				a := v.accounts[i]
				return app.Option().Value(a.ID).Text(a.Name).Selected(v.selectedAccount == a.ID)
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
	return app.Table().Class("txns").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Date"),
				app.Th().Text("Account"),
				app.Th().Text("Description"),
				app.Th().Class("right").Text("Amount"),
				app.Th().Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.txns).Slice(func(i int) app.UI {
				return v.renderRow(v.txns[i])
			}),
		),
	)
}

func (v *dashboardView) renderRow(t transaction) app.UI {
	amountClass := "amount pos"
	if strings.HasPrefix(strings.TrimSpace(t.Amount), "-") {
		amountClass = "amount neg"
	}
	desc := t.Payee
	if desc == "" {
		desc = t.Description
	}
	return app.Tr().Body(
		app.Td().Text(t.Date.Format("2006-01-02")),
		app.Td().Text(v.accountName(t.AccountID)),
		app.Td().Text(desc),
		app.Td().Class(amountClass).Text(t.Amount),
		app.Td().Body(v.pendingBadge(t.Pending)),
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

func (v *dashboardView) renderLoadMore() app.UI {
	if v.loading || !v.hasMore {
		return app.Text("")
	}
	label := "Load more"
	if v.loadingMore {
		label = "Loading…"
	}
	return app.Div().Class("loadmore").Body(
		app.Button().Class("btn").Text(label).OnClick(v.onLoadMore).Disabled(v.loadingMore),
	)
}
