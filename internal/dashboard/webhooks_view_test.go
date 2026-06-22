package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestIsAdminTokenRequired: only a 503 statusError (the requireConfiguredToken
// refusal on an unsecured instance) is treated as "needs a dashboard token".
func TestIsAdminTokenRequired(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"503 status error", statusError{op: "GET /api/v1/webhooks", status: http.StatusServiceUnavailable}, true},
		{"wrapped 503", fmt.Errorf("load webhooks: %w", statusError{op: "GET /api/v1/webhooks", status: http.StatusServiceUnavailable}), true},
		{"401 status error", statusError{op: "GET /api/v1/webhooks", status: http.StatusUnauthorized}, false},
		{"500 status error", statusError{op: "GET /api/v1/webhooks", status: http.StatusInternalServerError}, false},
		{"plain error", errors.New("connection refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isAdminTokenRequired(tc.err); got != tc.want {
			t.Errorf("%s: isAdminTokenRequired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStatusErrorMessage: the typed error keeps the original "<op>: status <code>"
// message the error banners render, so the change is invisible to other callers.
func TestStatusErrorMessage(t *testing.T) {
	got := statusError{op: "GET /api/v1/webhooks", status: 503}.Error()
	if want := "GET /api/v1/webhooks: status 503"; got != want {
		t.Fatalf("statusError message = %q, want %q", got, want)
	}
}

// TestWebhooksEmptyStateTokenHint: when the list could not load because the
// instance is unsecured, the page shows a calm "set a dashboard token" hint, not
// the red error banner and not the generic "no webhooks yet" copy.
func TestWebhooksEmptyStateTokenHint(t *testing.T) {
	v := &webhooksView{tokenRequired: true}

	list := printUI(t, v.renderList())
	if !strings.Contains(list, "Set a dashboard token to manage webhooks") {
		t.Fatalf("token-required empty state missing the hint\nHTML:\n%s", list)
	}
	if strings.Contains(list, "No webhooks yet") {
		t.Fatalf("token-required empty state must not show the generic empty copy\nHTML:\n%s", list)
	}

	// The red error banner is driven by errMsg, which stays empty in this case.
	if err := printUI(t, v.renderError()); strings.Contains(err, "Error:") {
		t.Fatalf("token-required state must not render an error banner\nHTML:\n%s", err)
	}
}

// TestWebhooksEmptyStateDefault: a secured-but-empty instance (load succeeded,
// no webhooks) shows the normal empty state, never the token hint.
func TestWebhooksEmptyStateDefault(t *testing.T) {
	v := &webhooksView{}
	html := printUI(t, v.renderList())
	if !strings.Contains(html, "No webhooks yet") {
		t.Fatalf("empty state missing the generic copy\nHTML:\n%s", html)
	}
	if strings.Contains(html, "Set a dashboard token") {
		t.Fatalf("empty state must not show the token hint when not token-gated\nHTML:\n%s", html)
	}
}
