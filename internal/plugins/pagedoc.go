package plugins

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// This file defines the declarative page document a plugin's OnPageRender /
// OnPageAction hook returns. A page is DATA, never markup or code: the dashboard
// walks the block list and renders each through go-app's text-safe primitives, so
// a plugin can extend the UI without shipping any frontend code (and without any
// XSS surface). ValidatePageDoc is the server-side trust boundary: whatever a
// plugin VM hands back is parsed, bounds-checked, and re-marshalled into the
// normalized form before it ever reaches a browser.

// PageRequest is the payload handed to the page hooks. A render carries no
// action; an action carries the id of the pressed button (declared by a previous
// render) plus that button's params.
type PageRequest struct {
	Plugin string            `json:"plugin"`
	Action string            `json:"action,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// PageDoc is the normalized page: an optional title (defaulting to the
// manifest's ui.title in the dashboard) and an ordered list of blocks.
type PageDoc struct {
	Title  string      `json:"title,omitempty"`
	Blocks []PageBlock `json:"blocks"`
}

// PageBlock is one renderable unit. Type selects the shape; only the fields for
// that type are meaningful (the rest stay empty after normalization):
//
//	heading  – Text
//	text     – Text
//	stat     – Label, Value, Hint
//	keyvalue – Items
//	table    – Columns, Rows
//	actions  – Actions (buttons that POST back to OnPageAction)
//	form     – ID, Fields, SubmitLabel (inputs that POST back to OnPageAction)
//	divider  – nothing
type PageBlock struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Label   string         `json:"label,omitempty"`
	Value   flexString     `json:"value,omitempty"`
	Hint    string         `json:"hint,omitempty"`
	Items   []PageKV       `json:"items,omitempty"`
	Columns []string       `json:"columns,omitempty"`
	Rows    [][]flexString `json:"rows,omitempty"`
	Actions []PageAction   `json:"actions,omitempty"`

	// Form fields (Type == "form"). Submitting POSTs {ID, params} back to
	// OnPageAction exactly like a button press, with every field's current value
	// in params under the field's name — so a plugin can collect settings from
	// the user and persist them with kasas.set_config.
	ID          string      `json:"id,omitempty"`
	Fields      []PageField `json:"fields,omitempty"`
	SubmitLabel string      `json:"submit_label,omitempty"`
}

// PageField is one input of a form block. Kind selects the control the
// dashboard renders; values always travel as strings (a toggle submits
// "true"/"false"), matching action params.
type PageField struct {
	Name        string     `json:"name"`
	Label       string     `json:"label"`
	Kind        string     `json:"kind,omitempty"` // "text" (default), "number", "toggle", "select"
	Value       flexString `json:"value,omitempty"`
	Placeholder string     `json:"placeholder,omitempty"`
	Help        string     `json:"help,omitempty"`
	Options     []string   `json:"options,omitempty"` // select only
}

// PageKV is one row of a keyvalue block.
type PageKV struct {
	Key   string     `json:"key"`
	Value flexString `json:"value"`
}

// PageAction is one button of an actions block. Pressing it POSTs {ID, Params}
// back to the plugin's OnPageAction hook, which returns the refreshed page.
type PageAction struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Style  string            `json:"style,omitempty"` // "", "primary", or "danger"
	Params map[string]string `json:"params,omitempty"`
}

// flexString accepts a string, number, bool, or null from the plugin (a Lua
// count or JS boolean is natural in a stat or table cell) and normalizes it to a
// string, so the dashboard only ever sees strings.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case nil:
		*f = ""
	case string:
		*f = flexString(x)
	case bool:
		*f = flexString(strconv.FormatBool(x))
	case float64:
		*f = flexString(strconv.FormatFloat(x, 'g', -1, 64))
	default:
		return fmt.Errorf("value must be a string, number, bool, or null")
	}
	return nil
}

// Page document bounds. They are generous for a dashboard page while keeping a
// misbehaving plugin from flooding the API response or the browser.
const (
	maxPageDocBytes = 256 << 10 // raw JSON returned by the hook
	maxPageBlocks   = 200
	maxPageTextLen  = 4096 // heading/text content
	maxPageFieldLen = 200  // title, stat label/hint, kv keys, column names, button labels
	maxPageValueLen = 1024 // stat values, kv values, table cells
	maxPageKVItems  = 200
	maxPageColumns  = 16
	maxPageRows     = 1000
	maxPageActions  = 16
	maxActionParams = 16
	// maxFormFields matches maxActionParams: a form's fields become exactly the
	// params of its OnPageAction submission.
	maxFormFields   = maxActionParams
	maxFieldOptions = 50 // options of one select field
)

// pageFieldKinds enumerates the valid form-field kinds ("" normalizes to "text").
var pageFieldKinds = map[string]bool{
	"text":   true,
	"number": true,
	"toggle": true,
	"select": true,
}

// actionIDRE constrains an action id to a slug, mirroring plugin names: the id
// round-trips through the dashboard and back into the action endpoint's payload.
var actionIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// pageBlockKinds enumerates the valid block types. An unknown type is a hard
// error (not skipped) so a plugin author finds the typo immediately; forward
// compatibility across kasas versions is the registry's compatibility story, not
// silent dropping.
var pageBlockKinds = map[string]bool{
	"heading":  true,
	"text":     true,
	"stat":     true,
	"keyvalue": true,
	"table":    true,
	"actions":  true,
	"form":     true,
	"divider":  true,
}

// ValidatePageDoc parses, bounds-checks, and normalizes a page document returned
// by a plugin hook, returning the canonical JSON to serve to the dashboard.
// Re-marshalling (rather than echoing the plugin's bytes) drops unknown fields
// and guarantees the wire shape matches PageDoc exactly.
func ValidatePageDoc(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("page hook returned nothing (expected a page document)")
	}
	if len(raw) > maxPageDocBytes {
		return nil, fmt.Errorf("page document too large (%d bytes, max %d)", len(raw), maxPageDocBytes)
	}
	var doc PageDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid page document: %w", err)
	}
	if err := doc.validate(); err != nil {
		return nil, err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode page document: %w", err)
	}
	return out, nil
}

func (d *PageDoc) validate() error {
	if len(d.Title) > maxPageFieldLen {
		return fmt.Errorf("page title too long (max %d)", maxPageFieldLen)
	}
	if len(d.Blocks) == 0 {
		return fmt.Errorf("page document has no blocks")
	}
	if len(d.Blocks) > maxPageBlocks {
		return fmt.Errorf("too many blocks (%d, max %d)", len(d.Blocks), maxPageBlocks)
	}
	for i := range d.Blocks {
		if err := d.Blocks[i].validate(); err != nil {
			return fmt.Errorf("block %d (%s): %w", i, d.Blocks[i].Type, err)
		}
	}
	return nil
}

func (b *PageBlock) validate() error {
	if !pageBlockKinds[b.Type] {
		return fmt.Errorf("unknown block type %q", b.Type)
	}
	switch b.Type {
	case "heading", "text":
		if b.Text == "" {
			return fmt.Errorf("text is required")
		}
		if len(b.Text) > maxPageTextLen {
			return fmt.Errorf("text too long (max %d)", maxPageTextLen)
		}
	case "stat":
		if b.Label == "" || b.Value == "" {
			return fmt.Errorf("label and value are required")
		}
		if len(b.Label) > maxPageFieldLen || len(b.Hint) > maxPageFieldLen {
			return fmt.Errorf("label/hint too long (max %d)", maxPageFieldLen)
		}
		if len(b.Value) > maxPageValueLen {
			return fmt.Errorf("value too long (max %d)", maxPageValueLen)
		}
	case "keyvalue":
		if len(b.Items) == 0 {
			return fmt.Errorf("items are required")
		}
		if len(b.Items) > maxPageKVItems {
			return fmt.Errorf("too many items (%d, max %d)", len(b.Items), maxPageKVItems)
		}
		for _, it := range b.Items {
			if it.Key == "" || len(it.Key) > maxPageFieldLen {
				return fmt.Errorf("item keys are required and at most %d characters", maxPageFieldLen)
			}
			if len(it.Value) > maxPageValueLen {
				return fmt.Errorf("item value too long (max %d)", maxPageValueLen)
			}
		}
	case "table":
		if err := b.validateTable(); err != nil {
			return err
		}
	case "actions":
		if err := b.validateActions(); err != nil {
			return err
		}
	case "form":
		if err := b.validateForm(); err != nil {
			return err
		}
	}
	b.clearUnusedFields()
	return nil
}

func (b *PageBlock) validateForm() error {
	if !actionIDRE.MatchString(b.ID) {
		return fmt.Errorf("invalid form id %q (must match %s)", b.ID, actionIDRE.String())
	}
	if len(b.SubmitLabel) > maxPageFieldLen {
		return fmt.Errorf("submit_label too long (max %d)", maxPageFieldLen)
	}
	if len(b.Fields) == 0 {
		return fmt.Errorf("fields are required")
	}
	if len(b.Fields) > maxFormFields {
		return fmt.Errorf("too many fields (%d, max %d)", len(b.Fields), maxFormFields)
	}
	seen := make(map[string]bool, len(b.Fields))
	for i := range b.Fields {
		f := &b.Fields[i]
		if !actionIDRE.MatchString(f.Name) {
			return fmt.Errorf("invalid field name %q (must match %s)", f.Name, actionIDRE.String())
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate field name %q", f.Name)
		}
		seen[f.Name] = true
		if f.Label == "" || len(f.Label) > maxPageFieldLen {
			return fmt.Errorf("field labels are required and at most %d characters", maxPageFieldLen)
		}
		if f.Kind == "" {
			f.Kind = "text"
		}
		if !pageFieldKinds[f.Kind] {
			return fmt.Errorf("unknown field kind %q (text, number, toggle, or select)", f.Kind)
		}
		if len(f.Value) > maxPageValueLen {
			return fmt.Errorf("field value too long (max %d)", maxPageValueLen)
		}
		if len(f.Placeholder) > maxPageFieldLen || len(f.Help) > maxPageFieldLen {
			return fmt.Errorf("field placeholder/help too long (max %d)", maxPageFieldLen)
		}
		if f.Kind == "select" {
			if len(f.Options) == 0 {
				return fmt.Errorf("select field %q needs options", f.Name)
			}
			if len(f.Options) > maxFieldOptions {
				return fmt.Errorf("too many options on field %q (%d, max %d)", f.Name, len(f.Options), maxFieldOptions)
			}
			for _, o := range f.Options {
				if o == "" || len(o) > maxPageValueLen {
					return fmt.Errorf("options of field %q must be non-empty and at most %d characters", f.Name, maxPageValueLen)
				}
			}
		} else if len(f.Options) > 0 {
			return fmt.Errorf("field %q has options but is not a select", f.Name)
		}
	}
	return nil
}

func (b *PageBlock) validateTable() error {
	if len(b.Columns) == 0 {
		return fmt.Errorf("columns are required")
	}
	if len(b.Columns) > maxPageColumns {
		return fmt.Errorf("too many columns (%d, max %d)", len(b.Columns), maxPageColumns)
	}
	for _, c := range b.Columns {
		if len(c) > maxPageFieldLen {
			return fmt.Errorf("column name too long (max %d)", maxPageFieldLen)
		}
	}
	if len(b.Rows) > maxPageRows {
		return fmt.Errorf("too many rows (%d, max %d)", len(b.Rows), maxPageRows)
	}
	for _, row := range b.Rows {
		if len(row) > len(b.Columns) {
			return fmt.Errorf("row has %d cells but the table has %d columns", len(row), len(b.Columns))
		}
		for _, cell := range row {
			if len(cell) > maxPageValueLen {
				return fmt.Errorf("cell too long (max %d)", maxPageValueLen)
			}
		}
	}
	return nil
}

func (b *PageBlock) validateActions() error {
	if len(b.Actions) == 0 {
		return fmt.Errorf("actions are required")
	}
	if len(b.Actions) > maxPageActions {
		return fmt.Errorf("too many actions (%d, max %d)", len(b.Actions), maxPageActions)
	}
	for _, a := range b.Actions {
		if !actionIDRE.MatchString(a.ID) {
			return fmt.Errorf("invalid action id %q (must match %s)", a.ID, actionIDRE.String())
		}
		if a.Label == "" || len(a.Label) > maxPageFieldLen {
			return fmt.Errorf("action labels are required and at most %d characters", maxPageFieldLen)
		}
		switch a.Style {
		case "", "primary", "danger":
		default:
			return fmt.Errorf("invalid action style %q (\"\", \"primary\", or \"danger\")", a.Style)
		}
		if len(a.Params) > maxActionParams {
			return fmt.Errorf("too many action params (%d, max %d)", len(a.Params), maxActionParams)
		}
		for k, v := range a.Params {
			if k == "" || len(k) > maxPageFieldLen || len(v) > maxPageValueLen {
				return fmt.Errorf("action param keys/values are required and bounded (%d/%d)", maxPageFieldLen, maxPageValueLen)
			}
		}
	}
	return nil
}

// clearUnusedFields zeroes the fields the block's type does not use, so the
// normalized wire form never carries stray data a plugin happened to set.
func (b *PageBlock) clearUnusedFields() {
	keep := struct{ text, stat, items, table, actions, form bool }{}
	switch b.Type {
	case "heading", "text":
		keep.text = true
	case "stat":
		keep.stat = true
	case "keyvalue":
		keep.items = true
	case "table":
		keep.table = true
	case "actions":
		keep.actions = true
	case "form":
		keep.form = true
	}
	if !keep.text {
		b.Text = ""
	}
	if !keep.stat {
		b.Label, b.Value, b.Hint = "", "", ""
	}
	if !keep.items {
		b.Items = nil
	}
	if !keep.table {
		b.Columns, b.Rows = nil, nil
	}
	if !keep.actions {
		b.Actions = nil
	}
	if !keep.form {
		b.ID, b.Fields, b.SubmitLabel = "", nil, ""
	}
}
