package auth

import "testing"

func TestHasPermission_SecretHasAll(t *testing.T) {
	allPerms := []Permission{
		PermCustomerRead, PermCustomerWrite, PermPricingRead, PermPricingWrite,
		PermSubscriptionRead, PermSubscriptionWrite, PermUsageRead, PermUsageWrite,
		PermInvoiceRead, PermInvoiceWrite, PermDunningRead, PermDunningWrite,
		PermAPIKeyRead, PermAPIKeyWrite,
		PermTestClockWrite,
	}
	for _, p := range allPerms {
		if !HasPermission(KeyTypeSecret, p) {
			t.Errorf("secret should have %s", p)
		}
	}
	// Secret should NOT have tenant management
	if HasPermission(KeyTypeSecret, PermTenantWrite) {
		t.Error("secret should NOT have tenant:write")
	}
}

func TestHasPermission_PublishableRestricted(t *testing.T) {
	// Publishable keys ship in browsers and get NO tenant-wide scopes —
	// authenticate-only. Two regressions guarded here:
	//   - the pre-FEAT-5-readiness write leak (customer:write + usage:write let
	//     any visitor create customers / fake usage), and
	//   - the read leak (customer:read in a browser key exposes all-customer
	//     PII, every invoice PDF, tenant revenue via /analytics, and CSV
	//     exports) — tenant-wide reads are the same exposure class as writes.
	notHas := []Permission{
		PermCustomerRead, PermUsageRead, PermSubscriptionRead, PermInvoiceRead,
		PermCustomerWrite, PermUsageWrite,
		PermPricingWrite, PermSubscriptionWrite, PermInvoiceWrite, PermDunningWrite, PermAPIKeyWrite, PermTenantWrite,
	}
	for _, p := range notHas {
		if HasPermission(KeyTypePublishable, p) {
			t.Errorf("publishable should NOT have %s", p)
		}
	}
}

func TestHasPermission_PlatformOnlyTenants(t *testing.T) {
	if !HasPermission(KeyTypePlatform, PermTenantRead) {
		t.Error("platform should have tenant:read")
	}
	if !HasPermission(KeyTypePlatform, PermTenantWrite) {
		t.Error("platform should have tenant:write")
	}
	if HasPermission(KeyTypePlatform, PermCustomerRead) {
		t.Error("platform should NOT have customer:read")
	}
}

func TestHasPermission_UnknownType(t *testing.T) {
	if HasPermission("nonexistent", PermCustomerRead) {
		t.Error("unknown type should have no permissions")
	}
}

func TestAllPermissions(t *testing.T) {
	secretPerms := AllPermissions(KeyTypeSecret)
	if len(secretPerms) != 15 {
		t.Errorf("secret should have 15 permissions, got %d", len(secretPerms))
	}

	pubPerms := AllPermissions(KeyTypePublishable)
	if len(pubPerms) != 0 {
		t.Errorf("publishable should have 0 permissions (authenticate-only), got %d", len(pubPerms))
	}

	platformPerms := AllPermissions(KeyTypePlatform)
	if len(platformPerms) != 2 {
		t.Errorf("platform should have 2 permissions, got %d", len(platformPerms))
	}
}
