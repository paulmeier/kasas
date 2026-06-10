package dashboard

import (
	"context"
	"strconv"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// settingsEditing is an embeddable mixin shared by the Settings and Sources
// pages: it renders one editable control per setting (by kind), saves and
// resets overrides through the API, and tracks in-flight state. Dependencies
// are injected via initSettingsEditing hooks so the mixin never redeclares
// fields promoted from chrome (e.g. client).
type settingsEditing struct {
	settingBusy string // key whose save/reset is in flight
	settingErr  string
	settingMsg  string

	// Injected hooks (initSettingsEditing): the API client, and a callback that
	// adopts a saved/reset setting into the view's local state.
	settingClient  func() *apiClient
	settingApplied func(ctx app.Context, st settingItem, restartRequired bool)
}

func (e *settingsEditing) initSettingsEditing(client func() *apiClient, applied func(ctx app.Context, st settingItem, restartRequired bool)) {
	e.settingClient = client
	e.settingApplied = applied
}

// settingInputID is the stable DOM id of a setting's text input/textarea, so
// its value can be read on save (the input is uncontrolled while typing).
func settingInputID(key string) string { return "setting-input-" + key }

// renderSettingMessages shows the shared save/reset feedback line.
func (e *settingsEditing) renderSettingMessages() app.UI {
	switch {
	case e.settingErr != "":
		return app.Div().Class("error").Text("Error: " + e.settingErr)
	case e.settingMsg != "":
		return app.Div().Class("settings-ok").Text(e.settingMsg)
	default:
		return app.Text("")
	}
}

// renderSettingRow renders one setting: title with state chips, the control for
// its kind, and its help text.
func (e *settingsEditing) renderSettingRow(s settingItem) app.UI {
	head := []app.UI{app.Span().Class("setting-title").Text(s.Title)}
	if s.Overridden {
		head = append(head, app.Span().Class("badge setting-chip overridden").Title("Set from the dashboard/API; overrides the config file and environment.").Text("overridden"))
	}
	if s.RestartRequired {
		head = append(head, app.Span().Class("badge setting-chip restart").Title("Takes effect after kasas restarts.").Text("restart pending"))
	}

	rows := []app.UI{
		app.Div().Class("setting-head").Body(head...),
		e.renderSettingControl(s),
	}
	if s.Help != "" {
		rows = append(rows, app.P().Class("settings-help setting-help").Text(s.Help))
	}
	return app.Div().Class("setting-row").Body(rows...)
}

// renderSettingControl renders the kind-appropriate control. Booleans and enums
// save immediately on change; text-like kinds save via their button (the input
// is uncontrolled while typing, read from the DOM on save).
func (e *settingsEditing) renderSettingControl(s settingItem) app.UI {
	busy := e.settingBusy == s.Key

	switch {
	case s.Kind == "bool":
		return app.Div().Class("form-row").Body(
			app.Label().Class("setting-toggle").Body(
				app.Input().
					Type("checkbox").
					Checked(s.Value == "true").
					Disabled(busy).
					OnChange(func(ctx app.Context, _ app.Event) {
						e.saveSetting(ctx, s.Key, strconv.FormatBool(ctx.JSSrc().Get("checked").Bool()))
					}),
				app.Span().Text(boolToggleText(s.Value == "true")),
			),
			e.renderResetButton(s, busy),
		)

	case len(s.Enum) > 0:
		return app.Div().Class("form-row").Body(
			app.Select().
				Class("account-select").
				Disabled(busy).
				OnChange(func(ctx app.Context, _ app.Event) {
					e.saveSetting(ctx, s.Key, ctx.JSSrc().Get("value").String())
				}).
				Body(
					app.Range(s.Enum).Slice(func(i int) app.UI {
						v := s.Enum[i]
						return app.Option().Value(v).Text(v).Selected(s.Value == v)
					}),
				),
			e.renderResetButton(s, busy),
		)

	case s.Kind == "json":
		return app.Div().Body(
			app.Textarea().
				ID(settingInputID(s.Key)).
				Class("settings-input setting-textarea").
				Rows(4).
				Placeholder("[]").
				Text(s.Value),
			app.Div().Class("form-row").Body(
				app.Button().
					Class("btn").
					Text(saveLabel(busy)).
					Disabled(busy).
					OnClick(func(ctx app.Context, _ app.Event) {
						e.saveSetting(ctx, s.Key, domInputValue(settingInputID(s.Key)))
					}),
				e.renderResetButton(s, busy),
			),
		)

	case s.Secret:
		placeholder := "Paste value"
		if s.Set {
			placeholder = "•••••• (set — paste to replace)"
		}
		return app.Div().Class("form-row").Body(
			app.Input().
				ID(settingInputID(s.Key)).
				Class("settings-input").
				Type("password").
				Placeholder(placeholder).
				AutoComplete(false),
			app.Button().
				Class("btn").
				Text(saveLabel(busy)).
				Disabled(busy).
				OnClick(func(ctx app.Context, _ app.Event) {
					val := domInputValue(settingInputID(s.Key))
					if val == "" {
						e.settingErr = "Enter a value first (use reset to clear it)."
						ctx.Update()
						return
					}
					e.saveSetting(ctx, s.Key, val)
				}),
			e.renderResetButton(s, busy),
		)

	default: // string, int, duration
		return app.Div().Class("form-row").Body(
			app.Input().
				ID(settingInputID(s.Key)).
				Class("settings-input").
				Type("text").
				Value(s.Value).
				AutoComplete(false),
			app.Button().
				Class("btn").
				Text(saveLabel(busy)).
				Disabled(busy).
				OnClick(func(ctx app.Context, _ app.Event) {
					e.saveSetting(ctx, s.Key, domInputValue(settingInputID(s.Key)))
				}),
			e.renderResetButton(s, busy),
		)
	}
}

// renderResetButton offers "reset" for an overridden setting: it removes the
// stored override so the config file / environment value applies again.
func (e *settingsEditing) renderResetButton(s settingItem, busy bool) app.UI {
	if !s.Overridden {
		return app.Text("")
	}
	return app.Button().
		Class("btn btn-sm setting-reset").
		Title("Remove the stored override; the config file / environment value applies after the next restart.").
		Text("Reset").
		Disabled(busy).
		OnClick(func(ctx app.Context, _ app.Event) { e.resetSetting(ctx, s.Key) })
}

func boolToggleText(on bool) string {
	if on {
		return "Enabled"
	}
	return "Disabled"
}

// saveSetting persists one setting value and adopts the server's normalized
// result into the view via the settingApplied hook.
func (e *settingsEditing) saveSetting(ctx app.Context, key, value string) {
	if e.settingBusy != "" {
		return
	}
	e.settingBusy = key
	e.settingErr = ""
	e.settingMsg = ""
	ctx.Update()

	ctx.Async(func() {
		res, err := e.settingClient().setSetting(context.Background(), key, value)
		ctx.Dispatch(func(ctx app.Context) {
			e.settingBusy = ""
			if err != nil {
				e.settingErr = "Could not save " + key + ": " + err.Error()
				ctx.Update()
				return
			}
			e.settingMsg = "Saved " + key + ". The change is permanent" + restartSuffix(res.RestartRequired)
			if res.Setting.Secret {
				clearDomInput(settingInputID(key))
			}
			e.settingApplied(ctx, res.Setting, res.RestartRequired)
			ctx.Update()
		})
	})
}

// resetSetting removes one stored override.
func (e *settingsEditing) resetSetting(ctx app.Context, key string) {
	if e.settingBusy != "" {
		return
	}
	e.settingBusy = key
	e.settingErr = ""
	e.settingMsg = ""
	ctx.Update()

	ctx.Async(func() {
		res, err := e.settingClient().resetSetting(context.Background(), key)
		ctx.Dispatch(func(ctx app.Context) {
			e.settingBusy = ""
			if err != nil {
				e.settingErr = "Could not reset " + key + ": " + err.Error()
				ctx.Update()
				return
			}
			e.settingMsg = "Reset " + key + " to its config value" + restartSuffix(res.RestartRequired)
			e.settingApplied(ctx, res.Setting, res.RestartRequired)
			ctx.Update()
		})
	})
}

func restartSuffix(restart bool) string {
	if restart {
		return " — restart kasas to apply."
	}
	return "."
}

// restartPrompt is an embeddable mixin showing the "restart required" banner
// and driving the in-place restart: it asks the server to re-exec, waits for it
// to come back (the open /api/v1/auth endpoint), and reloads the page.
type restartPrompt struct {
	restartNeeded bool
	restarting    bool
	restartErr    string
}

// renderRestartBanner renders the pending-restart banner. client is passed at
// render time (mixins must not redeclare promoted fields like chrome's client).
func (p *restartPrompt) renderRestartBanner(client func() *apiClient) app.UI {
	switch {
	case p.restartErr != "":
		return app.Div().Class("update-banner err").Body(
			app.Span().Class("update-text").Text("Restart failed: " + p.restartErr),
		)
	case p.restarting:
		return app.Div().Class("update-banner").Body(
			app.Span().Class("update-text").Text("Restarting kasas… this page reloads when it is back."),
		)
	case p.restartNeeded:
		return app.Div().Class("update-banner").Body(
			app.Span().Class("update-text").Text("Setting changes are saved and waiting for a restart to take effect."),
			app.Button().
				Class("btn btn-update").
				Text("Restart kasas").
				OnClick(func(ctx app.Context, _ app.Event) { p.startRestart(ctx, client()) }),
		)
	default:
		return app.Text("")
	}
}

// startRestart triggers the in-place restart and polls until the server is
// reachable again, then reloads the page so the UI reflects the new config.
func (p *restartPrompt) startRestart(ctx app.Context, client *apiClient) {
	if p.restarting {
		return
	}
	p.restarting = true
	p.restartErr = ""
	ctx.Update()

	ctx.Async(func() {
		if err := client.restartServer(context.Background()); err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				p.restarting = false
				p.restartErr = err.Error()
				ctx.Update()
			})
			return
		}
		// Give the old process a moment to re-exec, then wait for the new one.
		time.Sleep(2 * time.Second)
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := client.authStatus(context.Background()); err == nil {
				ctx.Dispatch(func(ctx app.Context) {
					app.Window().Get("location").Call("reload")
				})
				return
			}
			time.Sleep(1500 * time.Millisecond)
		}
		ctx.Dispatch(func(ctx app.Context) {
			p.restarting = false
			p.restartErr = "kasas did not come back in time — reload the page manually."
			ctx.Update()
		})
	})
}
