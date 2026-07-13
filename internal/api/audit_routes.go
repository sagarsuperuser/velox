package api

import "strings"

// ---------------------------------------------------------------------------
// The route-audit registry (ADR-090, final step).
//
// ONE central table declaring, for EVERY mutating route in the live chi route
// table, where that route's audit evidence comes from. It replaces the deleted
// catch-all middleware, whose "safety net" was the disease: it wrote a row for
// any mutating /v1 request a handler hadn't claimed, INFERRING the action and
// resource from the URL and sniffing the response body for a label — fabricating
// false permanent records in an append-only compliance log (ADR-090 RC2), while
// silently covering NOTHING outside the /v1 block (RC1).
//
// Coverage is now DECLARED, not inferred:
//
//   - explicit — the route emits a real, typed audit row (an in-tx LogInTx
//     emission on the business transaction, or a residual own-tx Logger.Log).
//     Every entry below was verified by reading the emitting code.
//   - exempt   — the route deliberately writes no audit row. The reason is a
//     CLOSED enum, and the note records WHY plus any accepted loss.
//
// TestAuditRouteRegistry_MatchesLiveRoutes (audit_routes_test.go) walks the real
// router and two-way-diffs it against this table: an undeclared mutating route
// FAILS CI, and a stale entry FAILS CI. Adding a mutating route therefore forces
// an audit decision at review time — that is the whole point. At runtime the
// pure-observer detector (mw.AuditCoverage) reports any mutating 2xx that
// produced no audit row and is not declared exempt here.
// ---------------------------------------------------------------------------

// auditCoverage is how a route's audit evidence is produced.
type auditCoverage int

const (
	// auditExplicit: the route emits its own typed audit row.
	auditExplicit auditCoverage = iota + 1
	// auditExempt: the route writes no audit row, on purpose. Needs a reason.
	auditExempt
)

// exemptReason is a CLOSED enum. A new reason is a deliberate widening of what
// "un-audited" is allowed to mean, so it lands here, in review, with a note —
// never as a free-text excuse at the call site.
type exemptReason string

const (
	// reasonNonMutatingPreview: a read-only POST (POST only because the input
	// doesn't fit a query string). It computes and returns; it writes nothing.
	reasonNonMutatingPreview exemptReason = "non_mutating_preview"

	// reasonMachineIngest: high-volume machine ingest where the events table
	// IS the record. See the accepted loss recorded on each entry.
	reasonMachineIngest exemptReason = "machine_ingest"

	// reasonSystemEndpoint: not an operator action on tenant data (no tenant to
	// scope a row to).
	reasonSystemEndpoint exemptReason = "system_endpoint"

	// reasonWebhookOwned: an inbound provider webhook. Its EFFECTS are audited
	// by the in-tx emissions its handlers make; the delivery itself is recorded
	// in stripe_webhook_events.
	reasonWebhookOwned exemptReason = "webhook_owned"

	// reasonBootstrap: one-time, pre-tenant setup. There is no tenant (and no
	// actor) to attribute a row to until it succeeds.
	reasonBootstrap exemptReason = "bootstrap"
)

// routeKey is (HTTP method, canonical chi route pattern).
type routeKey struct {
	Method  string
	Pattern string
}

// auditDecl is one registry entry.
type auditDecl struct {
	Coverage auditCoverage
	// Reason is set iff Coverage == auditExempt.
	Reason exemptReason
	// Note names the emission site (explicit) or justifies the exemption and
	// records any accepted loss (exempt).
	Note string
}

func explicit(note string) auditDecl {
	return auditDecl{Coverage: auditExplicit, Note: note}
}

func exempt(reason exemptReason, note string) auditDecl {
	return auditDecl{Coverage: auditExempt, Reason: reason, Note: note}
}

// canonicalRoute normalizes a chi route pattern so the two sources of patterns
// agree on one key: chi.Walk's pattern string (what the arch test sees) and
// chi.RouteContext(r.Context()).RoutePattern() (what the detector sees at
// runtime). They differ in exactly two ways — Walk does a SINGLE
// strings.Replace("/*/", "/") pass, so a Mount("/", …) inside a Route(…) leaves
// a residual "/*/" ("/v1/auth/*/login"), and Walk does not trim a trailing slash
// (a subrouter's r.Post("/") arrives as "/v1/customers/"). RoutePattern() loops
// the replace and trims. Applying chi's own normalization to BOTH sides makes
// them meet.
//
// CRITICAL: do NOT also trim a trailing "/*". A 404 inside a mounted subtree
// yields RoutePattern() == "/v1/customers/*", and trimming that would COLLAPSE
// it onto the real "/v1/customers" key — a miss masquerading as a covered route.
// Left intact, it matches no key at all, which is the honest answer.
func canonicalRoute(p string) string {
	for strings.Contains(p, "/*/") {
		p = strings.ReplaceAll(p, "/*/", "/")
	}
	if p != "/" {
		p = strings.TrimSuffix(p, "//")
		p = strings.TrimSuffix(p, "/")
	}
	if p == "" {
		return "/"
	}
	return p
}

// isMutatingMethod reports whether a method can change state. GET/HEAD/OPTIONS
// egress is a separate concern (ADR-090 read-egress auditing), not this gate's.
func isMutatingMethod(m string) bool {
	switch m {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// auditRouteExempt reports whether (method, canonical pattern) is declared
// exempt. The runtime detector consults this before reporting an uncovered
// mutation; anything not in the table is NOT exempt (fail loud, not silent).
func auditRouteExempt(method, pattern string) bool {
	d, ok := auditRouteRegistry[routeKey{Method: method, Pattern: canonicalRoute(pattern)}]
	return ok && d.Coverage == auditExempt
}

// auditRouteRegistry is the table. Keys are canonical (method, RoutePattern).
//
// Populated by walking the live router (chi.Walk over NewServer) and reading
// the emitting code for every mutating entry — not by guessing. Each explicit
// note names the emitter, so a reviewer can check the claim in one grep.
var auditRouteRegistry = map[routeKey]auditDecl{
	// --- system ---------------------------------------------------------
	// /metrics is registered with r.Handle, which chi expands to ALL 9
	// methods — so the route walk legitimately yields POST/PUT/PATCH/DELETE
	// /metrics. They are declared rather than filtered: silently skipping
	// r.Handle routes would make the registry blind to any future mALL route.
	{"POST", "/metrics"}:   exempt(reasonSystemEndpoint, "Prometheus scrape endpoint; r.Handle expands to all 9 methods. No tenant, no operator action."),
	{"PUT", "/metrics"}:    exempt(reasonSystemEndpoint, "same r.Handle expansion as POST /metrics."),
	{"PATCH", "/metrics"}:  exempt(reasonSystemEndpoint, "same r.Handle expansion as POST /metrics."),
	{"DELETE", "/metrics"}: exempt(reasonSystemEndpoint, "same r.Handle expansion as POST /metrics."),

	{"POST", "/v1/bootstrap"}: exempt(reasonBootstrap, "One-time pre-tenant setup: runs only while zero tenants exist, and there is no tenant/actor to attribute a row to until it succeeds. The tenant it creates carries tenant.Service's own create row (ADR-090 PR4)."),

	// --- inbound webhook ------------------------------------------------
	{"POST", "/v1/webhooks/stripe/{endpoint_id}"}: exempt(reasonWebhookOwned, "Inbound Stripe delivery. Its EFFECTS are audited by the in-tx emissions the webhook handlers make (payment.Stripe → LogInTx: PM flips, refund-status flips, settles); the delivery itself is recorded in stripe_webhook_events (the idempotency + replay record). A row per delivery would duplicate that table without adding an actor."),

	// --- machine ingest -------------------------------------------------
	// ACCEPTED LOSS (all four): "which credential ingested this event" is
	// unrecoverable — usage_events carries no key_id column. The events tables
	// are the record of WHAT was ingested, but not BY WHOM. Closure trigger:
	// the first billing dispute that turns on provenance. Fixing it means a
	// key_id column on usage_events (per-event attribution), not a per-request
	// audit row: at ingest volumes one audit row per call would outweigh the
	// events themselves.
	{"POST", "/v1/usage-events"}:       exempt(reasonMachineIngest, "High-volume metering ingest; usage_events IS the record. Accepted loss: no key_id on usage_events ⇒ ingesting credential unrecoverable."),
	{"POST", "/v1/usage-events/batch"}: exempt(reasonMachineIngest, "Batch form of the same ingest. Same accepted loss."),
	// NOT exempt, deliberately: backfill is an OPERATOR action on the money
	// path. Inserting backdated usage changes what a customer is billed for a
	// period that may already have closed — nothing like the machine metering
	// above. usage.Service.Backfill emits on the ingest tx (ADR-090).
	{"POST", "/v1/usage-events/backfill"}:      explicit("usage.Service.Backfill → IngestAudited (in-tx): action=create/usage_event, metadata.action=usage_backfilled with the backdated timestamp."),
	{"POST", "/v1/integrations/litellm/spend"}: exempt(reasonMachineIngest, "LiteLLM proxy spend callback — one POST per LLM call, funnels into the same usage_events writer. Same accepted loss."),

	// --- non-mutating previews ------------------------------------------
	{"POST", "/v1/invoices/create_preview"}: exempt(reasonNonMutatingPreview, "Read-only estimate; POST only because the composer payload doesn't fit a query string. Writes nothing (billing/create_preview_handler.go calls audit.MarkSkip)."),
	{"POST", "/v1/recipes/{key}/preview"}:   exempt(reasonNonMutatingPreview, "Read-only recipe render — returns the objects an instantiate WOULD create. Writes nothing (recipe/handler.go calls audit.MarkSkip)."),

	// --- auth / session (outside the old catch-all's reach entirely) ----
	{"POST", "/v1/auth/login"}:                  explicit("user.Handler.login → Log(login, user). Failed logins are pre-auth: no tenant to scope a row to, so they stay in the security log."),
	{"POST", "/v1/auth/logout"}:                 explicit("user.Handler.logout → Log(logout, user). A stale/absent cookie revokes nothing and declares MarkSkip."),
	{"POST", "/v1/auth/mode"}:                   explicit("user.Handler.setMode → Log(mode_changed, user) — live/test plane switch."),
	{"POST", "/v1/auth/password-reset/request"}: explicit("user.Handler.requestPasswordReset → Log(password_reset_requested) on a matched account. A non-match (and the per-address throttle) writes nothing and declares MarkSkip — the fixed 200 is the enumeration defence."),
	{"POST", "/v1/auth/password-reset/confirm"}: explicit("user.Handler.confirmPasswordReset → Log(password_reset_completed, user)."),
	{"POST", "/v1/auth/accept-invite"}:          explicit("dashmembers.Handler.acceptInvite → Log(member.joined, user)."),
	{"POST", "/v1/members/invite"}:              explicit("dashmembers.Handler.invite → Log(member.invited, user)."),
	{"DELETE", "/v1/members/invitations/{id}"}:  explicit("dashmembers.Handler.revokeInvitation → Log(member.invite_revoked, user)."),
	{"DELETE", "/v1/members/{userID}"}:          explicit("dashmembers.Handler.removeMember → Log(member.removed, user) — revokes the target's sessions."),

	// --- API keys -------------------------------------------------------
	{"POST", "/v1/api-keys"}:             explicit("auth.Handler.create → Log(create, api_key)."),
	{"DELETE", "/v1/api-keys/{id}"}:      explicit("auth.Handler.revoke → Log(revoke, api_key)."),
	{"POST", "/v1/api-keys/{id}/rotate"}: explicit("auth.Handler.rotate → Log(rotate, api_key)."),

	// --- tenants (platform) ---------------------------------------------
	{"POST", "/v1/tenants"}: explicit("tenant.Service.Create → LogInTx(create, tenant) on the tenants INSERT tx — the row lands in the NEW tenant's own log (ADR-090 PR4; this route mounts no catch-all and had NO trail before)."),

	// --- customers ------------------------------------------------------
	{"POST", "/v1/customers"}:                                  explicit("customer.Service.Create → LogInTx(create, customer) on the customer's own tx."),
	{"PATCH", "/v1/customers/{id}"}:                            explicit("customer.Handler.update → Log(update, customer) with a field diff."),
	{"PUT", "/v1/customers/{id}/billing-profile"}:              explicit("customer.Handler.upsertBillingProfile → Log(update, customer)."),
	{"POST", "/v1/customers/{id}/rotate-cost-dashboard-token"}: explicit("customer.Handler.rotateCostDashboardToken → Log(rotate, customer) — never records the plaintext token."),

	// --- payment methods (operator surface) -----------------------------
	{"POST", "/v1/customers/{customer_id}/payment-methods/setup-session"}:    explicit("paymentmethods.Service.CreateSetupSession → Log(update, customer: setup_session_created)."),
	{"POST", "/v1/customers/{customer_id}/payment-methods/send-setup-email"}: explicit("paymentmethods.Handler.operatorSendSetupEmail → Log(update, customer: setup_link_sent). No recipient address on the row (GDPR erasure) — email_outbox is the delivery record."),
	{"POST", "/v1/customers/{customer_id}/payment-methods/{pmID}/default"}:   explicit("paymentmethods.Service.SetDefault → Log(update, payment_method)."),
	{"DELETE", "/v1/customers/{customer_id}/payment-methods/{pmID}"}:         explicit("paymentmethods.Service.Detach → Log(delete, payment_method) + Log(update) on any promoted new default."),

	// --- pricing --------------------------------------------------------
	{"POST", "/v1/meters"}:                                 explicit("pricing.Handler.createMeter → Log(create, meter)."),
	{"PATCH", "/v1/meters/{id}"}:                           explicit("pricing.Service.UpdateMeter → LogInTx(update, meter) on the meter's own tx."),
	{"POST", "/v1/meters/{meter_id}/pricing-rules"}:        explicit("pricing.Handler.upsertMeterPricingRule → Log(update, meter_pricing_rule)."),
	{"DELETE", "/v1/meters/{meter_id}/pricing-rules/{id}"}: explicit("pricing.Service.DeleteMeterPricingRule → LogInTx(delete, meter_pricing_rule). The catch-all recorded this as 'deleted meter {meter_id}' — ADR-090's headline fabrication."),
	{"POST", "/v1/plans"}:                                  explicit("pricing.Handler.createPlan → Log(create, plan)."),
	{"PATCH", "/v1/plans/{id}"}:                            explicit("pricing.Handler.updatePlan → Log(update, plan)."),
	{"POST", "/v1/rating-rules"}:                           explicit("pricing.Handler.createRatingRule → Log(create, rating_rule)."),
	{"POST", "/v1/price-overrides"}:                        explicit("pricing.Handler.createOverride → Log(create, price_override)."),
	{"DELETE", "/v1/price-overrides/{id}"}:                 explicit("pricing.Handler.deleteOverride → Log(delete, price_override)."),

	// --- recipes --------------------------------------------------------
	{"POST", "/v1/recipes/{key}/instantiate"}: explicit("recipe.Service.Instantiate → LogInTx(create, recipe) on the generate-plan tx (ADR-085 born-unique install). An idempotent re-apply installs nothing, emits nothing, and declares audit.MarkSkip on the badge-exists branch — a no-op deserves no record (the catch-all fabricated a 'created recipe' row for it)."),

	// --- provider costs (COGS) ------------------------------------------
	{"PUT", "/v1/provider-costs"}:         explicit("usage.ProviderCostHandler.upsert → LogInTx(update, provider_cost_rate) on the upsert tx."),
	{"DELETE", "/v1/provider-costs/{id}"}: explicit("usage.ProviderCostHandler.delete → LogInTx(delete, provider_cost_rate) on the delete tx."),

	// --- subscriptions ---------------------------------------------------
	{"POST", "/v1/subscriptions"}:                                      explicit("subscription.Handler.create → Log(create, subscription)."),
	{"POST", "/v1/subscriptions/{id}/activate"}:                        explicit("subscription.Handler.activate → Log(activate, subscription)."),
	{"POST", "/v1/subscriptions/{id}/cancel"}:                          explicit("subscription.Service.Cancel → LogInTx(cancel, subscription) on the cancel tx (single writer: operator route AND dunning's terminal cancel)."),
	{"POST", "/v1/subscriptions/{id}/schedule-cancel"}:                 explicit("subscription.Handler.scheduleCancel → Log(update, subscription: cancel_scheduled)."),
	{"DELETE", "/v1/subscriptions/{id}/scheduled-cancel"}:              explicit("subscription.Handler.clearScheduledCancel → Log(update, subscription: cancel_unscheduled)."),
	{"PUT", "/v1/subscriptions/{id}/pause-collection"}:                 explicit("subscription.Handler.pauseCollection → Log(update, subscription)."),
	{"DELETE", "/v1/subscriptions/{id}/pause-collection"}:              explicit("subscription.Handler.resumeCollection → Log(update, subscription)."),
	{"POST", "/v1/subscriptions/{id}/end-trial"}:                       explicit("subscription.Handler.endTrial → Log(update, subscription: trial_ended)."),
	{"POST", "/v1/subscriptions/{id}/extend-trial"}:                    explicit("subscription.Handler.extendTrial → Log(update, subscription: trial_extended)."),
	{"PUT", "/v1/subscriptions/{id}/billing-thresholds"}:               explicit("subscription.Handler.setBillingThresholds → Log(update, subscription)."),
	{"DELETE", "/v1/subscriptions/{id}/billing-thresholds"}:            explicit("subscription.Handler.clearBillingThresholds → Log(update, subscription)."),
	{"POST", "/v1/subscriptions/{id}/items"}:                           explicit("subscription.Handler.addItem → Log(update, subscription) + a proration_failed row on the fallback path."),
	{"PATCH", "/v1/subscriptions/{id}/items/{itemID}"}:                 explicit("subscription.Handler.updateItem → Log(subscription.item_updated)."),
	{"DELETE", "/v1/subscriptions/{id}/items/{itemID}"}:                explicit("subscription.Handler.removeItem → Log(update, subscription)."),
	{"DELETE", "/v1/subscriptions/{id}/items/{itemID}/pending-change"}: explicit("subscription.Handler.cancelPendingItemChange → Log(update, subscription: pending_change_canceled)."),

	// --- invoices --------------------------------------------------------
	{"POST", "/v1/invoices"}:                          explicit("invoice.Service.Create → LogInTx(create, invoice) on the invoice's own tx."),
	{"POST", "/v1/invoices/{id}/line-items"}:          explicit("invoice.Service.AddLineItem → LogInTx(line_item_added) on the invoice tx. The catch-all wrote 'create invoice {id}' here — a row asserting an invoice the operator never created."),
	{"POST", "/v1/invoices/{id}/finalize"}:            explicit("invoice.Service.Finalize → Log(finalize, invoice)."),
	{"POST", "/v1/invoices/{id}/void"}:                explicit("invoice.Service.Void → LogInTx(void, invoice) on the void coordinator tx (tax reversal + credit reversal share it)."),
	{"POST", "/v1/invoices/{id}/mark-uncollectible"}:  explicit("invoice.Service.MarkUncollectible → Log(update, invoice). Single service entry for operator + dunning terminal + resolve paths."),
	{"POST", "/v1/invoices/{id}/record-payment"}:      explicit("invoice.Service.RecordOfflinePayment → Log(update, invoice: payment_recorded)."),
	{"POST", "/v1/invoices/{id}/collect"}:             explicit("invoice.Handler.collectPayment → Log(collect, invoice) — the money-movement row."),
	{"POST", "/v1/invoices/{id}/refund"}:              explicit("invoice.Handler.refund → Log(refund, invoice); the credit note carries its own issued row."),
	{"POST", "/v1/invoices/{id}/retry-tax"}:           explicit("invoice.Handler.retryTax → Log(retry_tax, invoice)."),
	{"POST", "/v1/invoices/{id}/send"}:                explicit("invoice.Handler.sendEmail → Log(send, invoice)."),
	{"POST", "/v1/invoices/{id}/resend-setup-link"}:   explicit("invoice.Handler.resendSetupLink → Log(send, invoice: setup_link)."),
	{"POST", "/v1/invoices/{id}/rotate-public-token"}: explicit("invoice.Handler.rotatePublicToken → Log(rotate, invoice) — never records the token."),

	// --- credit notes -----------------------------------------------------
	{"POST", "/v1/credit-notes"}:                   explicit("creditnote.Service.Create → LogInTx(create, credit_note) on the draft tx."),
	{"POST", "/v1/credit-notes/{id}/issue"}:        explicit("creditnote.Service.Issue → LogInTx(credit_note.issued) on the coordinator tx (+ the grant's own row). A DEFERRED outcome mutates nothing and declares MarkSkip."),
	{"POST", "/v1/credit-notes/{id}/void"}:         explicit("creditnote.Service.Void → LogInTx(void, credit_note) on the draft→voided CAS tx."),
	{"POST", "/v1/credit-notes/{id}/retry-refund"}: explicit("creditnote.Service.RetryRefund → LogInTx on the refund-status persist tx, UNCONDITIONALLY: the audit-worthy fact is the operator action (a real cash-back request against the customer's payment), not the state transition, so a converged re-drive still records a row — carrying status_changed=false. Deliberately the OPPOSITE of ApplyRefundWebhook, where a no-op redelivery must record nothing."),
	{"POST", "/v1/credit-notes/{id}/send"}:         explicit("creditnote.Handler.sendEmail → Log(send, credit_note)."),

	// --- credits ----------------------------------------------------------
	{"POST", "/v1/credits/grant"}:  explicit("credit.Service.Grant → LogInTx(grant, credit) on the ledger tx (ADR-090's first domain)."),
	{"POST", "/v1/credits/adjust"}: explicit("credit.Service.Adjust → LogInTx(adjust, credit) on the ledger tx."),

	// --- billing ----------------------------------------------------------
	{"POST", "/v1/billing/run"}: explicit("billing.Handler.run → Log(run, billing: cycle_run_triggered) — the operator's TRIGGER row (the per-invoice finalize rows can't say who started the run). Residual own-tx by design: the route owns no tx. A failed emission is surfaced in the response and left unmarked, so the detector reports it."),

	// --- dunning ----------------------------------------------------------
	{"POST", "/v1/dunning/policies"}:                  explicit("dunning.Handler.createPolicy → Log(create, dunning_policy)."),
	{"PATCH", "/v1/dunning/policies/{id}"}:            explicit("dunning.Handler.updatePolicy → Log(update, dunning_policy)."),
	{"DELETE", "/v1/dunning/policies/{id}"}:           explicit("dunning.Handler.deletePolicy → Log(delete, dunning_policy)."),
	{"POST", "/v1/dunning/policies/{id}/set-default"}: explicit("dunning.Handler.setDefaultPolicy → Log(update, dunning_policy: set_default)."),
	{"POST", "/v1/dunning/runs/{id}/resolve"}:         explicit("dunning.Handler.resolveRun → Log(update, dunning_run) — operator's manual resolution."),

	// --- webhook endpoints / events --------------------------------------
	{"POST", "/v1/webhook-endpoints/endpoints"}:                    explicit("webhook.Handler.createEndpoint → Log(create, webhook_endpoint)."),
	{"PATCH", "/v1/webhook-endpoints/endpoints/{id}"}:              explicit("webhook.Handler.updateEndpoint → Log(update, webhook_endpoint)."),
	{"DELETE", "/v1/webhook-endpoints/endpoints/{id}"}:             explicit("webhook.Handler.deleteEndpoint → Log(delete, webhook_endpoint)."),
	{"POST", "/v1/webhook-endpoints/endpoints/{id}/rotate-secret"}: explicit("webhook.Handler.rotateSecret → Log(rotate, webhook_endpoint) — never records the secret."),
	{"POST", "/v1/webhook-endpoints/events/{id}/replay"}:           explicit("webhook.Handler.replayEvent → Log(update, webhook_event: replayed)."),
	{"POST", "/v1/webhook_events/{id}/replay"}:                     explicit("webhook.Handler.replayEventV2 → Log(update, webhook_event: replayed)."),

	// --- settings / Stripe connection -------------------------------------
	{"PUT", "/v1/settings"}:                         explicit("tenant.SettingsHandler.upsert → Log(update, setting) with a FIELD-LEVEL diff. A save that changes no field mutates nothing semantically and declares MarkSkip."),
	{"POST", "/v1/settings/stripe"}:                 explicit("tenantstripe.Handler.connect → Log(create, stripe_credentials) — never records the keys."),
	{"DELETE", "/v1/settings/stripe/{mode}"}:        explicit("tenantstripe.Handler.delete → Log(delete, stripe_credentials)."),
	{"PATCH", "/v1/settings/stripe/{mode}/webhook"}: explicit("tenantstripe.Handler.setWebhook → Log(rotate, stripe_credentials: webhook_secret_set) — never records the secret."),

	// --- test clocks -------------------------------------------------------
	{"POST", "/v1/test-clocks"}:                    explicit("testclock.Handler.create → Log(create, test_clock)."),
	{"POST", "/v1/test-clocks/{id}/advance"}:       explicit("testclock.Handler.advance → Log(update, test_clock: advance_requested)."),
	{"POST", "/v1/test-clocks/{id}/retry-advance"}: explicit("testclock.Handler.retryAdvance → Log(update, test_clock: advance_retried)."),
	{"DELETE", "/v1/test-clocks/{id}"}:             explicit("testclock.Handler.delete → Log(delete, test_clock). ADR-086: the audit row is the simulation's ONLY surviving record after teardown."),

	// --- checkout / public payment surfaces --------------------------------
	{"POST", "/v1/checkout/setup"}:                          explicit("payment.CheckoutHandler.createSetupSession → LogInTx on the session-claim tx."),
	{"POST", "/v1/public/invoices/{token}/checkout"}:        explicit("hostedInvoiceStripeAdapter (api/adapters.go) → LogInTx on the checkout-claim tx; actor is the CUSTOMER (auth.WithCustomerActor). Outside the old catch-all entirely."),
	{"POST", "/v1/public/payment-updates/{token}/checkout"}: explicit("payment.PublicPaymentHandler.createCheckoutSession → LogInTx on the token consume/restore txs; customer actor."),
}
