package dashboard

import "github.com/maxence-charriere/go-app/v10/pkg/app"

// rulesView is a placeholder for the future Rules feature (automatically tagging
// transactions). It has no behaviour yet beyond the shared chrome.
type rulesView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge
}

func (v *rulesView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
}

func (v *rulesView) Render() app.UI {
	return v.renderShell(navRules,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Rules"),
			app.Span().Class("page-subtitle").Text("Automatically tag transactions"),
		),
		app.Div().Class("placeholder").Body(
			app.P().Class("placeholder-title").Text("Rules are coming soon."),
			app.P().Text("This is where you'll define rules that tag transactions "+
				"automatically — for example, tagging everything from a given payee."),
		),
	)
}
