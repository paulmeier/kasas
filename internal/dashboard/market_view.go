package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// marketView is the Market page: the configured external market/reference series
// (ADR 0006), their cache freshness, and an inline sparkline of each one's daily
// closes. Admin actions (set the provider API key, define/remove a series, warm
// the cache) are offered inline; the rich charting lives in the desktop dashboard,
// this page is the management + at-a-glance surface.
type marketView struct {
	app.Compo
	chrome

	meta    marketSeriesList
	series  []marketSeries
	loading bool
	errMsg  string

	// "Define series" form.
	showAdd      bool
	formID       string
	formSymbol   string
	formKind     string
	formCurrency string
	formName     string
	formAdjusted bool
	saving       bool

	// Provider API key form.
	keyInput  string
	savingKey bool

	// Per-series points, loaded on demand when a row is expanded.
	points        map[string]marketPointsResp
	loadingPoints map[string]bool
	expanded      map[string]bool
}

var marketKinds = []string{"equity", "fund", "index", "fx", "crypto"}

func (v *marketView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.points = map[string]marketPointsResp{}
	v.loadingPoints = map[string]bool{}
	v.expanded = map[string]bool{}
	if v.formKind == "" {
		v.formKind = "equity"
	}
	if v.formCurrency == "" {
		v.formCurrency = "USD"
	}
	v.loadSeries(ctx)
}

func (v *marketView) loadSeries(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		list, err := v.client.listMarketSeries(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.meta = list
			v.series = list.Series
			ctx.Update()
		})
	})
}

func (v *marketView) onSetKey(ctx app.Context, _ app.Event) {
	key := strings.TrimSpace(v.keyInput)
	if key == "" {
		return
	}
	v.savingKey = true
	ctx.Update()
	ctx.Async(func() {
		_, err := v.client.setSourceCredential(context.Background(), "market", key)
		ctx.Dispatch(func(ctx app.Context) {
			v.savingKey = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.keyInput = ""
			v.loadSeries(ctx)
		})
	})
}

func (v *marketView) onAddSeries(ctx app.Context, _ app.Event) {
	p := marketSeriesPayload{
		ID:       strings.TrimSpace(v.formID),
		Symbol:   strings.TrimSpace(v.formSymbol),
		Kind:     v.formKind,
		Currency: strings.TrimSpace(v.formCurrency),
		Adjusted: v.formAdjusted,
		Name:     strings.TrimSpace(v.formName),
	}
	v.saving = true
	ctx.Update()
	ctx.Async(func() {
		_, err := v.client.addMarketSeries(context.Background(), p)
		ctx.Dispatch(func(ctx app.Context) {
			v.saving = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.showAdd = false
			v.formID, v.formSymbol, v.formName = "", "", ""
			v.formAdjusted = false
			v.loadSeries(ctx)
		})
	})
}

func (v *marketView) onRemove(ctx app.Context, id string) {
	ctx.Async(func() {
		err := v.client.removeMarketSeries(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			delete(v.expanded, id)
			delete(v.points, id)
			v.loadSeries(ctx)
		})
	})
}

func (v *marketView) onWarm(ctx app.Context, _ app.Event) {
	ctx.Async(func() {
		err := v.client.syncSource(context.Background(), "market")
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = err.Error()
			}
			ctx.Update()
		})
	})
}

func (v *marketView) onToggleView(ctx app.Context, id string) {
	if v.expanded[id] {
		v.expanded[id] = false
		ctx.Update()
		return
	}
	v.expanded[id] = true
	if _, ok := v.points[id]; ok {
		ctx.Update()
		return
	}
	v.loadingPoints[id] = true
	ctx.Update()
	ctx.Async(func() {
		pts, err := v.client.marketPoints(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.loadingPoints[id] = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.points[id] = pts
			// A View may have triggered a cold fetch; reflect the new freshness in the
			// row header without a full reload.
			for i := range v.series {
				if v.series[i].ID == id {
					v.series[i].AsOf = pts.AsOf
					v.series[i].Fresh = pts.Fresh
					v.series[i].Points = len(pts.Points)
					break
				}
			}
			ctx.Update()
		})
	})
}

func (v *marketView) Render() app.UI {
	return v.renderShell(navMarket,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Market data"),
			app.Span().Class("page-subtitle").Text("Benchmark indices, fund NAVs, FX & crypto — cached on demand"),
		),
		v.renderError(),
		v.renderProvider(),
		v.renderControls(),
		v.renderAddForm(),
		v.renderContent(),
	)
}

func (v *marketView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *marketView) renderProvider() app.UI {
	status := "not configured"
	statusClass := "badge src-manual"
	if v.meta.Configured {
		status = "connected"
		statusClass = "badge src-simplefin"
	}
	provider := v.meta.Provider
	if provider == "" {
		provider = "alphavantage"
	}
	return app.Div().Class("market-provider").Body(
		app.Div().Class("market-provider-status").Body(
			app.Span().Text("Provider: "+provider+" "),
			app.Span().Class(statusClass).Text(status),
		),
		app.Div().Class("market-key-form").Body(
			app.Input().Type("password").
				Placeholder("Paste provider API key").
				Value(v.keyInput).
				OnInput(func(ctx app.Context, _ app.Event) { v.keyInput = ctx.JSSrc().Get("value").String() }),
			app.Button().Class("btn btn-primary").Disabled(v.savingKey).
				Text(saveKeyLabel(v.savingKey)).
				OnClick(v.onSetKey),
		),
		app.P().Class("empty-hint").Text("A free Alpha Vantage key (alphavantage.co/support/#api-key) is stored on the server; series fetch on demand behind a daily cache."),
	)
}

func (v *marketView) renderControls() app.UI {
	return app.Div().Class("controls").Body(
		app.Button().Class("btn btn-secondary").Text("⟳ Warm cache").Title("Refresh every stale or cold series now").
			OnClick(v.onWarm),
		app.Span().Class("controls-spacer"),
		app.Button().Class("btn btn-primary").Text(addToggleLabel(v.showAdd)).
			OnClick(func(ctx app.Context, _ app.Event) { v.showAdd = !v.showAdd; ctx.Update() }),
	)
}

func (v *marketView) renderAddForm() app.UI {
	if !v.showAdd {
		return app.Text("")
	}
	return app.Div().Class("market-add-form card").Body(
		app.Div().Class("form-row").Body(
			labeledInput("Internal id", app.Input().Type("text").Placeholder("e.g. spy").Value(v.formID).
				OnInput(func(ctx app.Context, _ app.Event) { v.formID = ctx.JSSrc().Get("value").String() })),
			labeledInput("Symbol", app.Input().Type("text").Placeholder("e.g. SPY, EUR/USD, BTC").Value(v.formSymbol).
				OnInput(func(ctx app.Context, _ app.Event) { v.formSymbol = ctx.JSSrc().Get("value").String() })),
		),
		app.Div().Class("form-row").Body(
			labeledInput("Kind", app.Select().
				OnChange(func(ctx app.Context, _ app.Event) { v.formKind = ctx.JSSrc().Get("value").String() }).
				Body(app.Range(marketKinds).Slice(func(i int) app.UI {
					return app.Option().Value(marketKinds[i]).Text(marketKinds[i]).Selected(v.formKind == marketKinds[i])
				}))),
			labeledInput("Currency", app.Input().Type("text").Placeholder("USD").Value(v.formCurrency).
				OnInput(func(ctx app.Context, _ app.Event) { v.formCurrency = ctx.JSSrc().Get("value").String() })),
		),
		app.Div().Class("form-row").Body(
			labeledInput("Name (optional)", app.Input().Type("text").Placeholder("e.g. S&P 500 ETF").Value(v.formName).
				OnInput(func(ctx app.Context, _ app.Event) { v.formName = ctx.JSSrc().Get("value").String() })),
			app.Label().Class("market-adjusted").Body(
				app.Input().Type("checkbox").Checked(v.formAdjusted).
					OnChange(func(ctx app.Context, _ app.Event) { v.formAdjusted = ctx.JSSrc().Get("checked").Bool() }),
				app.Span().Text(" Total-return / adjusted (provider-dependent)"),
			),
		),
		app.Div().Class("form-actions").Body(
			app.Button().Class("btn btn-primary").Disabled(v.saving).Text(saveSeriesLabel(v.saving)).OnClick(v.onAddSeries),
		),
	)
}

func (v *marketView) renderContent() app.UI {
	if v.loading && len(v.series) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.series) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No series configured."),
			app.P().Class("empty-hint").Text(`Define a series above — e.g. id "spy", symbol "SPY", kind "equity" — then set the provider key. Points are fetched the first time you view them.`),
		)
	}
	return app.Div().Class("market-series-list").Body(
		app.Range(v.series).Slice(func(i int) app.UI {
			return v.renderSeries(v.series[i])
		}),
	)
}

func (v *marketView) renderSeries(s marketSeries) app.UI {
	freshClass, freshText := "badge src-manual", "stale"
	if s.Fresh {
		freshClass, freshText = "badge src-simplefin", "fresh"
	}
	asOf := s.AsOf
	if asOf == "" {
		asOf = "never fetched"
	}
	header := app.Div().Class("market-series-head").Body(
		app.Div().Class("market-series-id").Body(
			app.Strong().Text(s.ID),
			app.Span().Class("market-series-sym").Text(" "+s.Symbol+" · "+s.Kind+" · "+s.Currency),
		),
		app.Div().Class("market-series-meta").Body(
			app.Span().Class(freshClass).Text(freshText),
			app.Span().Class("market-series-asof").Text(" as of "+asOf+" · "+strconv.Itoa(s.Points)+" pts"),
		),
		app.Div().Class("row-actions").Body(
			app.Button().Type("button").Class("row-edit-btn").Title("View points").
				OnClick(func(ctx app.Context, _ app.Event) { v.onToggleView(ctx, s.ID) }).Text(viewLabel(v.expanded[s.ID])),
			app.Button().Type("button").Class("row-delete-btn").Title("Remove series").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRemove(ctx, s.ID) }).Text("🗑"),
		),
	)
	if !v.expanded[s.ID] {
		return app.Div().Class("market-series card").Body(header)
	}
	return app.Div().Class("market-series card").Body(header, v.renderPoints(s))
}

func (v *marketView) renderPoints(s marketSeries) app.UI {
	if v.loadingPoints[s.ID] {
		return app.Div().Class("status").Text("Loading points…")
	}
	pts, ok := v.points[s.ID]
	if !ok || len(pts.Points) == 0 {
		return app.Div().Class("empty-hint").Text("No points cached yet.")
	}
	label := "price"
	if s.Adjusted {
		label = "total return"
	}
	return app.Div().Class("market-series-detail").Body(
		sparkline(pts.Points),
		app.Div().Class("market-series-caption").Text(fmt.Sprintf("%s · daily close (%s) · this is a benchmark series, not your account's return", s.Symbol, label)),
		v.renderRecentTable(pts.Points),
	)
}

func (v *marketView) renderRecentTable(points []marketPoint) app.UI {
	// Show the most recent few closes, newest first.
	n := len(points)
	limit := 8
	if n < limit {
		limit = n
	}
	recent := make([]marketPoint, 0, limit)
	for i := 0; i < limit; i++ {
		recent = append(recent, points[n-1-i])
	}
	return app.Table().Class("txns market-points-table").Body(
		app.THead().Body(app.Tr().Body(app.Th().Text("Date"), app.Th().Class("right").Text("Close"))),
		app.TBody().Body(app.Range(recent).Slice(func(i int) app.UI {
			return app.Tr().Body(
				app.Td().Text(recent[i].Date),
				app.Td().Class("amount").Text(recent[i].Value),
			)
		})),
	)
}

// sparkline renders a tiny inline-SVG line of a series' recent closes. Up vs down
// over the window colors the line green or red. Daily granularity, last 60 points.
func sparkline(points []marketPoint) app.UI {
	if len(points) > 60 {
		points = points[len(points)-60:]
	}
	vals := make([]float64, 0, len(points))
	for _, p := range points {
		f, err := strconv.ParseFloat(strings.TrimSpace(p.Value), 64)
		if err != nil {
			continue
		}
		vals = append(vals, f)
	}
	if len(vals) < 2 {
		return app.Text("")
	}
	min, max := vals[0], vals[0]
	for _, f := range vals {
		if f < min {
			min = f
		}
		if f > max {
			max = f
		}
	}
	const w, h = 320.0, 56.0
	span := max - min
	if span == 0 {
		span = 1
	}
	var b strings.Builder
	for i, f := range vals {
		x := float64(i) / float64(len(vals)-1) * w
		y := h - (f-min)/span*h
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	color := "#10b981"
	if vals[len(vals)-1] < vals[0] {
		color = "#f43f5e"
	}
	return app.Raw(fmt.Sprintf(
		`<svg class="market-sparkline" viewBox="0 0 %g %g" preserveAspectRatio="none"><polyline fill="none" stroke="%s" stroke-width="1.5" points="%s"/></svg>`,
		w, h, color, b.String()))
}

// labeledInput wraps a control with a small label, for the add-series form.
func labeledInput(label string, control app.UI) app.UI {
	return app.Label().Class("market-field").Body(
		app.Span().Class("market-field-label").Text(label),
		control,
	)
}

func saveKeyLabel(saving bool) string {
	if saving {
		return "Saving…"
	}
	return "Save key"
}

func saveSeriesLabel(saving bool) string {
	if saving {
		return "Adding…"
	}
	return "Add series"
}

func addToggleLabel(open bool) string {
	if open {
		return "Cancel"
	}
	return "+ Define series"
}

func viewLabel(expanded bool) string {
	if expanded {
		return "Hide"
	}
	return "View"
}
