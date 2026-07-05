package domain

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/sagarsuperuser/velox/internal/errs"
)

// MaxAdditionalEmails caps a customer's CC list (ADR-082). Enforced at
// the service layer — the column stores ciphertext, so no DB CHECK can
// count entries.
const MaxAdditionalEmails = 10

// ValidateEmail is the shared tier-1 syntax check for recipient
// addresses: RFC-parseable plus a hard requirement that the domain
// contains a dot (ParseAddress alone allows local-only addresses like
// `foo@bar` — RFC-fine, useless for SMTP delivery). Tier-6
// (bounce-driven suppression) catches what survives.
func ValidateEmail(field, email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil {
		return errs.Invalid(field, "invalid email")
	}
	at := strings.LastIndex(addr.Address, "@")
	if at < 0 {
		return errs.Invalid(field, "invalid email")
	}
	domainPart := addr.Address[at+1:]
	if !strings.Contains(domainPart, ".") || strings.HasSuffix(domainPart, ".") {
		return errs.Invalid(field, "invalid email: domain must contain a dot")
	}
	return nil
}

// NormalizeAdditionalEmails validates and canonicalizes a CC list
// (ADR-082): trim → ValidateEmail → keep the BARE lowercased addr-spec
// (mail.ParseAddress accepts display-name forms like "Bob <bob@x.com>";
// keeping raw input would let display-name/CRLF material reach the Cc
// header builder) → case-insensitive dedupe → reject entries equal to
// the primary → cap. Returns nil for an effectively-empty list. Shared
// by the customer service (stored list) and the send handlers
// (per-send override) so both enforce identical rules.
func NormalizeAdditionalEmails(list []string, primaryEmail string) ([]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	primary := strings.ToLower(strings.TrimSpace(primaryEmail))
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, raw := range list {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if err := ValidateEmail("additional_emails", e); err != nil {
			return nil, err
		}
		addr, _ := mail.ParseAddress(e)
		norm := strings.ToLower(addr.Address)
		if norm == primary && primary != "" {
			return nil, errs.Invalid("additional_emails", "must not repeat the primary email — it always receives billing emails")
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	if len(out) > MaxAdditionalEmails {
		return nil, errs.Invalid("additional_emails", fmt.Sprintf("at most %d additional emails are allowed", MaxAdditionalEmails))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
