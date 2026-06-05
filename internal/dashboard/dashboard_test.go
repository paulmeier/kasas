package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestAllAccountsOptionHasValue guards against a regression where the
// "All accounts" <option> used Value(""). go-app drops empty value attributes,
// so the option rendered as `<option>All accounts</option>` — and a valueless
// <option> reports its text ("All accounts") as its DOM value. Selecting it then
// sent account_id=All+accounts as a filter, matching no account and showing
// "No transactions." The option must carry a real, non-empty value.
func TestAllAccountsOptionHasValue(t *testing.T) {
	v := &dashboardView{
		accounts: []account{{ID: "ACT-1", Name: "Checking"}},
	}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderControls())
	html := buf.String()

	if strings.Contains(html, "<option>All accounts</option>") {
		t.Fatalf("All accounts option rendered without a value attribute; "+
			"selecting it sends the text as account_id.\nHTML:\n%s", html)
	}
	if !strings.Contains(html, `value="`+allAccountsValue+`"`) {
		t.Fatalf("expected All accounts option to use value %q.\nHTML:\n%s", allAccountsValue, html)
	}
}

// TestAllAccountsValueNotAnAccountID ensures the sentinel can never collide with
// a real account id, since onAccountChange maps it back to "" (no filter).
func TestAllAccountsValueNotAnAccountID(t *testing.T) {
	if allAccountsValue == "" {
		t.Fatal("allAccountsValue must be non-empty so go-app keeps the value attribute")
	}
}
