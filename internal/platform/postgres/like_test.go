package postgres

import "testing"

func TestEscapeLike(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"acme", "acme"},
		{"100%", `100\%`},
		{"a_b", `a\_b`},
		{`back\slash`, `back\\slash`},
		{`%_\`, `\%\_\\`},
		{"", ""},
		{"INV-2026-0042", "INV-2026-0042"},
	}
	for _, c := range cases {
		if got := EscapeLike(c.in); got != c.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
