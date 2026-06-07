package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// accountEditing is an embeddable mixin driving the "Add account" / "Edit account"
// modal. Like transactionEditing it holds form state and receives its actions as
// hook closures wired in the host's OnMount. Only manually-created accounts are
// edited/deleted through it (the host shows the affordances only for source=="manual"
// accounts); a brand-new account is always manual.
type accountEditing struct {
	acctEditOpen bool
	acctEditID   string // "" = create mode; otherwise the id being edited
	acctForm     accountForm
	acctSaving   bool
	acctEditErr  string

	acctCreate      func(ctx context.Context, p accountPayload) (account, error)
	acctUpdate      func(ctx context.Context, id string, p accountPayload) (account, error)
	acctDelete      func(ctx context.Context, id string) error
	acctAfterChange func(ctx app.Context) // reload accounts (and the table) after a change
	acctReportError func(string)
}

type accountForm struct {
	name     string
	currency string
	balance  string
}

func (v *accountEditing) openCreateAccount(ctx app.Context) {
	v.acctEditID = ""
	v.acctForm = accountForm{currency: "USD", balance: "0"}
	v.acctEditErr = ""
	v.acctEditOpen = true
	ctx.Update()
}

func (v *accountEditing) openEditAccount(ctx app.Context, a account) {
	v.acctEditID = a.ID
	v.acctForm = accountForm{name: a.Name, currency: a.Currency, balance: a.Balance}
	v.acctEditErr = ""
	v.acctEditOpen = true
	ctx.Update()
}

func (v *accountEditing) closeAccountEditor(ctx app.Context, _ app.Event) {
	v.acctEditOpen = false
	v.acctEditErr = ""
	ctx.Update()
}

func (v *accountEditing) onSaveAccount(ctx app.Context, _ app.Event) {
	if v.acctSaving {
		return
	}
	p := accountPayload{
		Name:     strings.TrimSpace(v.acctForm.name),
		Currency: strings.TrimSpace(v.acctForm.currency),
		Balance:  strings.TrimSpace(v.acctForm.balance),
	}
	id := v.acctEditID
	v.acctSaving = true
	v.acctEditErr = ""
	ctx.Update()
	ctx.Async(func() {
		var err error
		if id == "" {
			_, err = v.acctCreate(context.Background(), p)
		} else {
			_, err = v.acctUpdate(context.Background(), id, p)
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.acctSaving = false
			if err != nil {
				v.acctEditErr = err.Error()
				ctx.Update()
				return
			}
			v.acctEditOpen = false
			ctx.Update()
			if v.acctAfterChange != nil {
				v.acctAfterChange(ctx)
			}
		})
	})
}

// onDeleteAccount confirms (warning about the transaction cascade), deletes a manual
// account, and reloads. Bound to the per-card delete button (manual cards only).
func (v *accountEditing) onDeleteAccount(ctx app.Context, a account) {
	msg := "Delete account \"" + a.Name + "\" and ALL of its transactions? This cannot be undone."
	if !app.Window().Call("confirm", msg).Bool() {
		return
	}
	ctx.Async(func() {
		err := v.acctDelete(context.Background(), a.ID)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				if v.acctReportError != nil {
					v.acctReportError(err.Error())
				}
				ctx.Update()
				return
			}
			if v.acctAfterChange != nil {
				v.acctAfterChange(ctx)
			}
		})
	})
}

func (v *accountEditing) renderAccountEditor() app.UI {
	if !v.acctEditOpen {
		return app.Text("")
	}
	title := "New account"
	saveLabel := "Create"
	if v.acctEditID != "" {
		title = "Edit account"
		saveLabel = "Save"
	}
	if v.acctSaving {
		saveLabel = "Saving…"
	}

	var body []app.UI
	if v.acctEditErr != "" {
		body = append(body, app.Div().Class("form-error").Text(v.acctEditErr))
	}
	body = append(body,
		formField("Name",
			app.Input().Class("form-input").Type("text").Placeholder("Cash").
				Value(v.acctForm.name).
				OnInput(func(ctx app.Context, _ app.Event) { v.acctForm.name = ctx.JSSrc().Get("value").String() }),
		),
		formField("Currency",
			app.Input().Class("form-input").Type("text").Placeholder("USD").
				Value(v.acctForm.currency).
				OnInput(func(ctx app.Context, _ app.Event) { v.acctForm.currency = ctx.JSSrc().Get("value").String() }),
		),
		formField("Balance",
			app.Input().Class("form-input").Type("text").Placeholder("0.00").
				Value(v.acctForm.balance).
				OnInput(func(ctx app.Context, _ app.Event) { v.acctForm.balance = ctx.JSSrc().Get("value").String() }),
		),
		app.Div().Class("form-hint").Text("The balance is a value you maintain; kasas does not recompute it from this account's transactions."),
		app.Div().Class("form-actions").Body(
			app.Button().Class("btn").Text("Cancel").OnClick(v.closeAccountEditor),
			app.Button().Class("btn btn-primary").Text(saveLabel).Disabled(v.acctSaving).OnClick(v.onSaveAccount),
		),
	)

	return app.Div().Class("modal-overlay").OnClick(v.closeAccountEditor).Body(
		app.Div().Class("modal editor-modal").
			OnClick(func(ctx app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.H3().Class("modal-title").Text(title),
					app.Button().Class("modal-close").Text("×").OnClick(v.closeAccountEditor),
				),
				app.Div().Class("modal-body").Body(body...),
			),
	)
}
