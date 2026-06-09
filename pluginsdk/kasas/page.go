package kasas

// This file mirrors the declarative page document a dashboard-page plugin
// returns from OnPageRender / OnPageAction (see internal/plugins/pagedoc.go for
// the authoritative shapes and bounds). A page is data, never markup: kasas
// validates and re-marshals whatever the plugin returns before it reaches a
// browser.

// Page is the document a page hook returns: an optional title (defaulting to
// the manifest's ui.title) and an ordered list of blocks.
type Page struct {
	Title  string  `json:"title,omitempty"`
	Blocks []Block `json:"blocks"`
}

// Block is one renderable unit. Use the constructors (Heading, Stat, Table,
// Form, ...) rather than filling the struct by hand; only the fields belonging
// to a block's type are rendered.
type Block struct {
	Type    string     `json:"type"`
	Text    string     `json:"text,omitempty"`
	Label   string     `json:"label,omitempty"`
	Value   string     `json:"value,omitempty"`
	Hint    string     `json:"hint,omitempty"`
	Items   []KV       `json:"items,omitempty"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Actions []Action   `json:"actions,omitempty"`

	// Form fields (Type == "form").
	ID          string  `json:"id,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
	SubmitLabel string  `json:"submit_label,omitempty"`
}

// KV is one row of a keyvalue block.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Action is one button of an actions block. Pressing it invokes OnPageAction
// with this ID and Params. Style is "", "primary", or "danger".
type Action struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Style  string            `json:"style,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// Field is one input of a form block. Kind is "text" (default), "number",
// "toggle", or "select" (Options required for select). Submitted values arrive
// in the OnPageAction params as strings under the field's Name.
type Field struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind,omitempty"`
	Value       string   `json:"value,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// Heading renders a section heading.
func Heading(text string) Block { return Block{Type: "heading", Text: text} }

// Text renders a paragraph of plain text.
func Text(text string) Block { return Block{Type: "text", Text: text} }

// Stat renders one labeled metric with an optional hint line.
func Stat(label, value, hint string) Block {
	return Block{Type: "stat", Label: label, Value: value, Hint: hint}
}

// KeyValue renders a list of key/value rows.
func KeyValue(items ...KV) Block { return Block{Type: "keyvalue", Items: items} }

// Table renders a table with the given column headers and string rows.
func Table(columns []string, rows [][]string) Block {
	return Block{Type: "table", Columns: columns, Rows: rows}
}

// Actions renders a row of buttons that invoke OnPageAction when pressed.
func Actions(actions ...Action) Block { return Block{Type: "actions", Actions: actions} }

// Form renders an input form; submitting invokes OnPageAction with the form's
// id and every field's current value in params.
func Form(id, submitLabel string, fields ...Field) Block {
	return Block{Type: "form", ID: id, SubmitLabel: submitLabel, Fields: fields}
}

// Divider renders a horizontal separator.
func Divider() Block { return Block{Type: "divider"} }
