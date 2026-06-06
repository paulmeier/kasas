package api

import (
	"strings"
	"testing"
)

func TestRedactDSN(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
	}{
		"empty":                {"", ""},
		"url with password":    {"postgres://user:secret@db:5432/kasas?sslmode=disable", "postgres://user:xxxxx@db:5432/kasas?sslmode=disable"},
		"url without password": {"postgres://user@db:5432/kasas", "postgres://user@db:5432/kasas"},
		"keyword form":         {"host=db user=kasas password=secret dbname=kasas", "(set)"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactDSN(c.in)
			if got != c.want {
				t.Fatalf("redactDSN(%q) = %q, want %q", c.in, got, c.want)
			}
			if c.in != "" && strings.Contains(got, "secret") {
				t.Fatalf("redactDSN(%q) leaked the password: %q", c.in, got)
			}
		})
	}
}
