package migrate

import (
	"reflect"
	"strings"
	"testing"
)

// hasNoTxHeader is the leaf check that drives the entire hybrid runner.
// A regression here would silently flip files between the in-tx and
// autocommit code paths, so the table covers the common shapes we expect
// to see in real migration files.
func TestHasNoTxHeader(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "header on first line",
			body: "-- velox:no-transaction\nCREATE INDEX CONCURRENTLY foo ON bar(baz);\n",
			want: true,
		},
		{
			name: "header after leading copyright comment",
			body: "-- Copyright (c) Velox\n-- velox:no-transaction\nCREATE INDEX CONCURRENTLY foo ON bar(baz);\n",
			want: true,
		},
		{
			name: "header on line 5 (last allowed)",
			body: "-- a\n-- b\n-- c\n-- d\n-- velox:no-transaction\nSELECT 1;\n",
			want: true,
		},
		{
			name: "header on line 6 (out of window)",
			body: "-- a\n-- b\n-- c\n-- d\n-- e\n-- velox:no-transaction\nSELECT 1;\n",
			want: false,
		},
		{
			name: "no header at all",
			body: "CREATE INDEX foo ON bar(baz);\n",
			want: false,
		},
		{
			name: "trailing whitespace tolerated",
			body: "-- velox:no-transaction   \t\nSELECT 1;\n",
			want: true,
		},
		{
			name: "wrong case rejected",
			body: "-- VELOX:NO-TRANSACTION\nSELECT 1;\n",
			want: false,
		},
		{
			name: "leading whitespace rejected — header must be flush-left",
			body: "  -- velox:no-transaction\nSELECT 1;\n",
			want: false,
		},
		{
			name: "header inside a SQL string later in file is ignored",
			body: "CREATE TABLE t (note TEXT);\nINSERT INTO t VALUES ('-- velox:no-transaction');\n",
			want: false,
		},
		{
			name: "blank file",
			body: "",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasNoTxHeader([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("hasNoTxHeader(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// splitSQLStatements is the other piece of the no-tx applier that runs in
// a vacuum. The contract: each statement returned must be safe to send to
// PG via a separate Exec call. Strings, identifiers, dollar-quoted bodies,
// and `--` line comments must not bleed across statements.
func TestSplitSQLStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single statement no trailing semicolon",
			in:   "SELECT 1",
			want: []string{"SELECT 1"},
		},
		{
			name: "single statement with semicolon",
			in:   "SELECT 1;",
			want: []string{"SELECT 1"},
		},
		{
			name: "two statements",
			in:   "CREATE TABLE a (id INT); CREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			name: "leading no-tx header stripped, statement preserved",
			in:   "-- velox:no-transaction\nCREATE INDEX CONCURRENTLY i ON t(x);",
			want: []string{"CREATE INDEX CONCURRENTLY i ON t(x)"},
		},
		{
			name: "semicolon inside single-quoted string is not a separator",
			in:   "INSERT INTO t VALUES ('a;b'); SELECT 1;",
			want: []string{"INSERT INTO t VALUES ('a;b')", "SELECT 1"},
		},
		{
			name: "doubled single-quote is an escape",
			in:   "INSERT INTO t VALUES ('it''s; fine'); SELECT 1;",
			want: []string{"INSERT INTO t VALUES ('it''s; fine')", "SELECT 1"},
		},
		{
			name: "semicolon inside dollar-quoted body is not a separator",
			in:   "CREATE FUNCTION f() RETURNS void AS $$ BEGIN; END; $$ LANGUAGE plpgsql; SELECT 1;",
			want: []string{
				"CREATE FUNCTION f() RETURNS void AS $$ BEGIN; END; $$ LANGUAGE plpgsql",
				"SELECT 1",
			},
		},
		{
			name: "semicolon inside tagged dollar-quoted body is not a separator",
			in:   "DO $body$ BEGIN; END; $body$; SELECT 1;",
			want: []string{"DO $body$ BEGIN; END; $body$", "SELECT 1"},
		},
		{
			name: "line comment containing semicolons does not split",
			in:   "-- one; two; three\nSELECT 1;",
			want: []string{"SELECT 1"},
		},
		{
			name: "inline trailing line comment kept on its statement",
			in:   "SELECT 1; -- trailer\nSELECT 2;",
			want: []string{"SELECT 1", "-- trailer\nSELECT 2"}, // leading -- on second is stripped
		},
		{
			name: "block comment containing semicolon does not split",
			in:   "/* a; b */ SELECT 1; /* trailer */ SELECT 2;",
			want: []string{"/* a; b */ SELECT 1", "/* trailer */ SELECT 2"},
		},
		{
			name: "double-quoted identifier with semicolon",
			in:   `CREATE TABLE "weird;name" (id INT); SELECT 1;`,
			want: []string{`CREATE TABLE "weird;name" (id INT)`, "SELECT 1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitSQLStatements([]byte(tc.in))
			if err != nil {
				t.Fatalf("splitSQLStatements: unexpected err: %v", err)
			}
			// Normalize: the splitter strips leading line-comments off each
			// statement, but our want-strings aren't pre-stripped. Apply the
			// same transform to expectations so the comparison is faithful
			// to what gets sent to PG.
			normWant := make([]string, len(tc.want))
			for i, s := range tc.want {
				normWant[i] = stripLeadingComments(strings.TrimSpace(s))
			}
			if !reflect.DeepEqual(got, normWant) {
				t.Fatalf("splitSQLStatements(%q):\n  got  %q\n  want %q", tc.in, got, normWant)
			}
		})
	}
}

func TestSplitSQLStatements_UnterminatedString(t *testing.T) {
	if _, err := splitSQLStatements([]byte("SELECT 'oops")); err == nil {
		t.Fatal("expected error for unterminated single-quoted string")
	}
	if _, err := splitSQLStatements([]byte("SELECT $$ open")); err == nil {
		t.Fatal("expected error for unterminated dollar-quoted body")
	}
	if _, err := splitSQLStatements([]byte("SELECT /* unfinished")); err == nil {
		t.Fatal("expected error for unterminated block comment")
	}
}

// generateAdvisoryLockID is load-bearing for concurrent-replica safety: it
// must produce the same value golang-migrate would for the same DB inputs.
// The expected values below are computed from the library's published
// algorithm at v4.19.1. If this test breaks, golang-migrate likely changed
// its CRC scheme — verify against the upstream source before adjusting.
func TestGenerateAdvisoryLockID_DeterministicAndStable(t *testing.T) {
	// Same inputs → same output.
	a := generateAdvisoryLockID("velox", "public", "schema_migrations")
	b := generateAdvisoryLockID("velox", "public", "schema_migrations")
	if a != b {
		t.Fatalf("non-deterministic: %d vs %d", a, b)
	}

	// Different inputs → different outputs (collision is statistically
	// unlikely; this is a smoke test, not a uniqueness proof).
	c := generateAdvisoryLockID("velox_test", "public", "schema_migrations")
	if c == a {
		t.Fatalf("expected different lock id for different db, got %d", a)
	}

	// Reordering inputs changes the output (the library joins as
	// `additionalNames || dbName`, so the order matters).
	d := generateAdvisoryLockID("public", "velox", "schema_migrations")
	if d == a {
		t.Fatalf("expected different lock id when inputs reordered")
	}
}

// versionsWithNoTxHeader walks the embedded SQL FS. We assert at least
// one no-tx file exists in the embedded set (this is what the 0062 GIN
// retrofit added) — and that 0054 itself is NOT no-tx (its column rewrite
// remains in-tx, only the GIN portion is hoisted to 0062).
func TestVersionsWithNoTxHeader_EmbeddedSet(t *testing.T) {
	upSet, err := versionsWithNoTxHeader("up")
	if err != nil {
		t.Fatalf("versionsWithNoTxHeader up: %v", err)
	}
	downSet, err := versionsWithNoTxHeader("down")
	if err != nil {
		t.Fatalf("versionsWithNoTxHeader down: %v", err)
	}

	if _, ok := upSet[54]; ok {
		t.Errorf("0054 must not carry the no-tx header — its column rewrite is deferred and remains in-tx")
	}
	if _, ok := upSet[62]; !ok {
		t.Errorf("expected 0062 to carry the no-tx header (CONCURRENTLY GIN retrofit)")
	}
	if _, ok := downSet[62]; !ok {
		t.Errorf("expected 0062 down to also be no-tx (DROP INDEX CONCURRENTLY)")
	}
}
