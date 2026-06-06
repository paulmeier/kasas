package dashboard

import (
	"encoding/json"
	"sort"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/paulmeier/kasas/internal/extensions"
)

// renderExtensionsCell renders a transaction's schema extensions as read-only,
// namespace-sorted chips ("key: value"). Extensions are app-owned metadata; the
// dashboard only displays them — writes happen via the REST API or the
// set_transaction_extensions MCP tool, so there is no inline editor here. Values
// are shown stringified (a JSON string by its text, other JSON by its compact
// form), with the full "key: value" in the title attribute for crowded rows.
// Shared by the Dashboard and Search result tables.
func renderExtensionsCell(t transaction) app.UI {
	keys := sortedExtKeys(t.Extensions)
	if len(keys) == 0 {
		return app.Td().Class("ext-cell")
	}
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		val := extensions.StringifyValue(t.Extensions[k])
		chips = append(chips, app.Span().Class("ext-chip").Title(k+": "+val).Body(
			app.Span().Class("ext-key").Text(k),
			app.Span().Class("ext-val").Text(val),
		))
	}
	return app.Td().Class("ext-cell").Body(app.Div().Class("ext-chips").Body(chips...))
}

// sortedExtKeys returns a transaction's extension keys in sorted order so chips
// render deterministically (Go map iteration is randomized). Sorting also groups
// keys by namespace, since a namespace is the dotted prefix.
func sortedExtKeys(ext map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(ext))
	for k := range ext {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
