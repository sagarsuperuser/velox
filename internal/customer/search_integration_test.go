package customer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sagarsuperuser/velox/internal/customer"
	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
	"github.com/sagarsuperuser/velox/internal/testutil"
)

// TestPostgresStore_ListSearch exercises the decrypt-and-match search
// path with PII encryption ENABLED — the property under test is that
// search matches display_name / email substrings even though those
// columns hold ciphertext in the database (SQL ILIKE can't see them).
func TestPostgresStore_ListSearch(t *testing.T) {
	db := testutil.SetupTestDB(t)
	store := customer.NewPostgresStore(db)
	enc, err := crypto.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	store.SetEncryptor(enc)
	ctx := postgres.WithLivemode(context.Background(), false)
	tenantID := testutil.CreateTestTenant(t, db, "Search")

	seed := []domain.Customer{
		{ExternalID: "acme_corp", DisplayName: "Acme Corporation", Email: "billing@acme.com"},
		{ExternalID: "globex", DisplayName: "Globex Inc", Email: "finance@globex.io"},
		{ExternalID: "initech", DisplayName: "Initech LLC", Email: "ap@initech.dev"},
	}
	for _, c := range seed {
		if _, err := store.Create(ctx, tenantID, c); err != nil {
			t.Fatalf("seed %s: %v", c.ExternalID, err)
		}
	}

	// Sanity: the at-rest value must be ciphertext, otherwise this test
	// silently degrades into a plaintext-scan test.
	var raw string
	rawTx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		t.Fatalf("bypass tx: %v", err)
	}
	err = rawTx.QueryRowContext(ctx,
		`SELECT display_name FROM customers WHERE tenant_id = $1 AND external_id = 'acme_corp'`, tenantID,
	).Scan(&raw)
	_ = rawTx.Rollback()
	if err != nil {
		t.Fatalf("read raw display_name: %v", err)
	}
	if strings.Contains(strings.ToLower(raw), "acme") {
		t.Fatalf("display_name stored as plaintext (%q) — encryption not active, test premise broken", raw)
	}

	cases := []struct {
		name, search string
		wantExtIDs   []string
	}{
		{"display name substring, case-insensitive", "ACME", []string{"acme_corp"}},
		{"email substring (encrypted column)", "finance@globex", []string{"globex"}},
		{"email domain fragment", ".dev", []string{"initech"}},
		{"external_id", "initech", []string{"initech"}},
		{"no match", "no-such-customer", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Search: tc.search})
			if err != nil {
				t.Fatalf("search %q: %v", tc.search, err)
			}
			if total != len(tc.wantExtIDs) || len(got) != len(tc.wantExtIDs) {
				t.Fatalf("search %q: got %d rows (total %d), want %d", tc.search, len(got), total, len(tc.wantExtIDs))
			}
			for i, want := range tc.wantExtIDs {
				if got[i].ExternalID != want {
					t.Errorf("search %q row %d: got %q, want %q", tc.search, i, got[i].ExternalID, want)
				}
			}
			// Rows must come back decrypted, not as ciphertext.
			for _, c := range got {
				if !strings.HasPrefix(c.DisplayName, "Acme") && !strings.HasPrefix(c.DisplayName, "Globex") && !strings.HasPrefix(c.DisplayName, "Initech") {
					t.Errorf("row %s display_name not decrypted: %q", c.ExternalID, c.DisplayName)
				}
			}
		})
	}

	// Search composes with the status filter (SQL side) — archive one
	// matching customer and assert it drops out.
	t.Run("composes with status filter", func(t *testing.T) {
		all, _, err := store.List(ctx, customer.ListFilter{TenantID: tenantID})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var acme domain.Customer
		for _, c := range all {
			if c.ExternalID == "acme_corp" {
				acme = c
			}
		}
		acme.Status = "archived"
		if _, err := store.Update(ctx, tenantID, acme); err != nil {
			t.Fatalf("archive: %v", err)
		}
		got, total, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Search: "acme", Status: "active"})
		if err != nil {
			t.Fatalf("search active: %v", err)
		}
		if total != 0 || len(got) != 0 {
			t.Errorf("archived customer should not match status=active search; got %d (total %d)", len(got), total)
		}
	})

	// Pagination over the matched set: total reflects all matches, the
	// window respects limit/offset.
	t.Run("paginates matched set", func(t *testing.T) {
		got, total, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Search: "@", Limit: 2})
		if err != nil {
			t.Fatalf("search paged: %v", err)
		}
		if total != 3 {
			t.Errorf("total: got %d, want 3 (every seeded email contains @)", total)
		}
		if len(got) != 2 {
			t.Errorf("page: got %d rows, want 2", len(got))
		}
		rest, _, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Search: "@", Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("search page 2: %v", err)
		}
		if len(rest) != 1 {
			t.Errorf("page 2: got %d rows, want 1", len(rest))
		}
	})

	// Go-side re-sort for encrypted sort keys: display_name asc must be
	// alphabetical on PLAINTEXT (ciphertext order would be arbitrary).
	t.Run("sorts by decrypted display_name", func(t *testing.T) {
		got, _, err := store.List(ctx, customer.ListFilter{TenantID: tenantID, Search: "@", Sort: "display_name", SortDir: "asc"})
		if err != nil {
			t.Fatalf("search sorted: %v", err)
		}
		for i := 1; i < len(got); i++ {
			if strings.ToLower(got[i-1].DisplayName) > strings.ToLower(got[i].DisplayName) {
				t.Errorf("display_name asc out of order: %q before %q", got[i-1].DisplayName, got[i].DisplayName)
			}
		}
	})
}
