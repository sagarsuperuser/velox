package auth

// Permission represents a specific API operation scope.
type Permission string

const (
	PermTenantRead  Permission = "tenant:read"
	PermTenantWrite Permission = "tenant:write"

	PermCustomerRead  Permission = "customer:read"
	PermCustomerWrite Permission = "customer:write"

	PermPricingRead  Permission = "pricing:read"
	PermPricingWrite Permission = "pricing:write"

	PermSubscriptionRead  Permission = "subscription:read"
	PermSubscriptionWrite Permission = "subscription:write"

	PermUsageRead  Permission = "usage:read"
	PermUsageWrite Permission = "usage:write"

	PermInvoiceRead  Permission = "invoice:read"
	PermInvoiceWrite Permission = "invoice:write"

	PermDunningRead  Permission = "dunning:read"
	PermDunningWrite Permission = "dunning:write"

	PermAPIKeyRead  Permission = "apikey:read"
	PermAPIKeyWrite Permission = "apikey:write"
)

// KeyType determines the prefix and permission set for an API key.
type KeyType string

const (
	KeyTypePlatform    KeyType = "platform"    // vlx_platform_ — tenant management
	KeyTypeSecret      KeyType = "secret"      // vlx_secret_   — full tenant access
	KeyTypePublishable KeyType = "publishable" // vlx_pub_      — restricted tenant access
)

func (kt KeyType) Prefix() string {
	switch kt {
	case KeyTypePlatform:
		return "vlx_platform_"
	case KeyTypePublishable:
		return "vlx_pub_"
	default:
		return "vlx_secret_"
	}
}

var keyPermissions = map[KeyType]map[Permission]bool{
	KeyTypePlatform: {
		PermTenantRead:  true,
		PermTenantWrite: true,
	},
	KeyTypeSecret: {
		PermCustomerRead:      true,
		PermCustomerWrite:     true,
		PermPricingRead:       true,
		PermPricingWrite:      true,
		PermSubscriptionRead:  true,
		PermSubscriptionWrite: true,
		PermUsageRead:         true,
		PermUsageWrite:        true,
		PermInvoiceRead:       true,
		PermInvoiceWrite:      true,
		PermDunningRead:       true,
		PermDunningWrite:      true,
		PermAPIKeyRead:        true,
		PermAPIKeyWrite:       true,
	},
	KeyTypePublishable: {
		PermCustomerRead:     true,
		PermCustomerWrite:    true,
		PermUsageRead:        true,
		PermUsageWrite:       true,
		PermSubscriptionRead: true,
		PermInvoiceRead:      true,
	},
}

func HasPermission(keyType KeyType, perm Permission) bool {
	perms, ok := keyPermissions[keyType]
	if !ok {
		return false
	}
	return perms[perm]
}

func AllPermissions(keyType KeyType) []Permission {
	perms, ok := keyPermissions[keyType]
	if !ok {
		return nil
	}
	result := make([]Permission, 0, len(perms))
	for p := range perms {
		result = append(result, p)
	}
	return result
}
