package dashboard

import (
	"context"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// transactionEditing is an embeddable mixin (like labelEditing / relationshipsViewing)
// that drives the "Add transaction" / "Edit transaction" modal. It holds the form
// state and gets its data + actions injected as hook closures in the host view's
// OnMount, so it never needs the host's concrete type. Only manually-created
// transactions are edited through it (the host shows the Edit affordance only for
// source=="manual" rows); a brand-new transaction is always manual.
type transactionEditing struct {
	txnEditOpen bool
	txnEditID   string // "" = create mode; otherwise the id being edited
	txnForm     transactionForm
	txnSaving   bool
	txnEditErr  string

	// Hooks wired by the host in OnMount.
	txnEditAccounts func() []account // accounts for the picker (all accounts)
	txnCreate       func(ctx context.Context, p transactionPayload) (transaction, error)
	txnUpdate       func(ctx context.Context, id string, p transactionPayload) (transaction, error)
	txnDelete       func(ctx context.Context, id string) error
	txnAfterChange  func(ctx app.Context) // reload the table after a successful save/delete
	txnReportError  func(string)          // surface a delete error on the page
}

// transactionForm is the editable state of the modal, mirroring transactionPayload
// (date as YYYY-MM-DD for the native date input).
type transactionForm struct {
	accountID   string
	amount      string
	date        string
	description string
	payee       string
	memo        string
	pending     bool
}

// openCreateTransaction opens the modal in create mode, defaulting the account to
// the one currently filtered (or the first account) and the date to today.
func (v *transactionEditing) openCreateTransaction(ctx app.Context, defaultAccountID string) {
	accts := v.txnEditAccounts()
	acctID := defaultAccountID
	if acctID == "" && len(accts) > 0 {
		acctID = accts[0].ID
	}
	v.txnEditID = ""
	v.txnForm = transactionForm{accountID: acctID, date: time.Now().Format("2006-01-02")}
	v.txnEditErr = ""
	v.txnEditOpen = true
	ctx.Update()
}

// openEditTransaction opens the modal pre-filled to edit an existing manual
// transaction.
func (v *transactionEditing) openEditTransaction(ctx app.Context, t transaction) {
	v.txnEditID = t.ID
	v.txnForm = transactionForm{
		accountID:   t.AccountID,
		amount:      t.Amount,
		date:        t.Date.Format("2006-01-02"),
		description: t.Description,
		payee:       t.Payee,
		memo:        t.Memo,
		pending:     t.Pending,
	}
	v.txnEditErr = ""
	v.txnEditOpen = true
	ctx.Update()
}

func (v *transactionEditing) closeTransactionEditor(ctx app.Context, _ app.Event) {
	v.txnEditOpen = false
	v.txnEditErr = ""
	ctx.Update()
}

func (v *transactionEditing) onSaveTransaction(ctx app.Context, _ app.Event) {
	if v.txnSaving {
		return
	}
	p := transactionPayload{
		AccountID:   v.txnForm.accountID,
		Amount:      strings.TrimSpace(v.txnForm.amount),
		Date:        strings.TrimSpace(v.txnForm.date),
		Description: strings.TrimSpace(v.txnForm.description),
		Payee:       strings.TrimSpace(v.txnForm.payee),
		Memo:        strings.TrimSpace(v.txnForm.memo),
		Pending:     v.txnForm.pending,
	}
	id := v.txnEditID
	v.txnSaving = true
	v.txnEditErr = ""
	ctx.Update()
	ctx.Async(func() {
		var err error
		if id == "" {
			_, err = v.txnCreate(context.Background(), p)
		} else {
			_, err = v.txnUpdate(context.Background(), id, p)
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.txnSaving = false
			if err != nil {
				v.txnEditErr = err.Error()
				ctx.Update()
				return
			}
			v.txnEditOpen = false
			ctx.Update()
			if v.txnAfterChange != nil {
				v.txnAfterChange(ctx)
			}
		})
	})
}

// onDeleteTransaction confirms, deletes a manual transaction, and reloads. Bound to
// the per-row delete button (only rendered for manual rows).
func (v *transactionEditing) onDeleteTransaction(ctx app.Context, t transaction) {
	if !app.Window().Call("confirm", "Delete this transaction? This cannot be undone.").Bool() {
		return
	}
	ctx.Async(func() {
		err := v.txnDelete(context.Background(), t.ID)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				if v.txnReportError != nil {
					v.txnReportError(err.Error())
				}
				ctx.Update()
				return
			}
			if v.txnAfterChange != nil {
				v.txnAfterChange(ctx)
			}
		})
	})
}

// renderTransactionEditor renders the modal, or nothing when closed.
func (v *transactionEditing) renderTransactionEditor() app.UI {
	if !v.txnEditOpen {
		return app.Text("")
	}
	title := "New transaction"
	saveLabel := "Create"
	if v.txnEditID != "" {
		title = "Edit transaction"
		saveLabel = "Save"
	}
	if v.txnSaving {
		saveLabel = "Saving…"
	}
	accts := v.txnEditAccounts()

	var body []app.UI
	if v.txnEditErr != "" {
		body = append(body, app.Div().Class("form-error").Text(v.txnEditErr))
	}
	if len(accts) == 0 {
		body = append(body, app.Div().Class("form-hint").Text("Create an account first, then add transactions to it."))
	} else {
		body = append(body,
			formField("Account",
				app.Select().Class("form-input").OnChange(func(ctx app.Context, _ app.Event) {
					v.txnForm.accountID = ctx.JSSrc().Get("value").String()
				}).Body(
					app.Range(accts).Slice(func(i int) app.UI {
						a := accts[i]
						return app.Option().Value(a.ID).Text(a.Name).Selected(a.ID == v.txnForm.accountID)
					}),
				),
			),
			formField("Amount",
				app.Input().Class("form-input").Type("text").Placeholder("-12.34").
					Value(v.txnForm.amount).
					OnInput(func(ctx app.Context, _ app.Event) { v.txnForm.amount = ctx.JSSrc().Get("value").String() }),
			),
			formField("Date",
				app.Input().Class("form-input").Type("date").
					Value(v.txnForm.date).
					OnInput(func(ctx app.Context, _ app.Event) { v.txnForm.date = ctx.JSSrc().Get("value").String() }),
			),
			formField("Description",
				app.Input().Class("form-input").Type("text").
					Value(v.txnForm.description).
					OnInput(func(ctx app.Context, _ app.Event) { v.txnForm.description = ctx.JSSrc().Get("value").String() }),
			),
			formField("Payee",
				app.Input().Class("form-input").Type("text").
					Value(v.txnForm.payee).
					OnInput(func(ctx app.Context, _ app.Event) { v.txnForm.payee = ctx.JSSrc().Get("value").String() }),
			),
			formField("Memo",
				app.Input().Class("form-input").Type("text").
					Value(v.txnForm.memo).
					OnInput(func(ctx app.Context, _ app.Event) { v.txnForm.memo = ctx.JSSrc().Get("value").String() }),
			),
			app.Label().Class("form-check").Body(
				app.Input().Type("checkbox").Checked(v.txnForm.pending).
					OnChange(func(ctx app.Context, _ app.Event) { v.txnForm.pending = ctx.JSSrc().Get("checked").Bool() }),
				app.Span().Text("Pending"),
			),
			app.Div().Class("form-actions").Body(
				app.Button().Class("btn").Text("Cancel").OnClick(v.closeTransactionEditor),
				app.Button().Class("btn btn-primary").Text(saveLabel).Disabled(v.txnSaving).OnClick(v.onSaveTransaction),
			),
		)
	}

	return app.Div().Class("modal-overlay").OnClick(v.closeTransactionEditor).Body(
		app.Div().Class("modal editor-modal").
			OnClick(func(ctx app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.H3().Class("modal-title").Text(title),
					app.Button().Class("modal-close").Text("×").OnClick(v.closeTransactionEditor),
				),
				app.Div().Class("modal-body").Body(body...),
			),
	)
}

// formField wraps a labeled control in the shared form-field layout. Shared by the
// transaction and account editors.
func formField(label string, control app.UI) app.UI {
	return app.Div().Class("form-field").Body(
		app.Label().Class("form-label").Text(label),
		control,
	)
}
