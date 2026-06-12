package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/sagarsuperuser/velox/internal/domain"
	"github.com/sagarsuperuser/velox/internal/errs"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// thresholdEval is the decision the scan reaches for one subscription. When
// CrossedAny is true the engine fires an early finalize via fireThreshold;
// otherwise the row is left alone until the next tick. RunningSubtotal and
// PerItemRunning are kept in the struct so the firing path can reuse the
// already-computed lines instead of re-aggregating after the decision.
type thresholdEval struct {
	CrossedAny      bool
	CrossedAmount   bool
	CrossedItem     bool
	CrossedItemID   string
	RunningSubtotal int64
	LineItems       []domain.InvoiceLineItem
	InvoiceCurrency string
}

// ScanThresholds finds every subscription with a billing threshold configured
// and fires an early-finalize invoice for each one whose in-cycle running
// total has crossed the cap. Called by the scheduler between the auto-charge
// retry sweep and the natural cycle scan so a threshold-fired invoice is
// resolved (charge + dunning entry) on the same tick it's emitted.
//
// Invariants:
//
//   - Idempotent under retry. The partial unique index on
//     invoices(tenant_id, subscription_id, billing_period_start) WHERE
//     billing_reason='threshold' guarantees at most one threshold invoice
//     per cycle. A second tick that observes the same crossed state lands
//     on errs.ErrAlreadyExists and short-circuits.
//
//   - Mode-scoped via ctx livemode. The scheduler fans out per-mode before
//     calling, so the candidate fetch (ListWithThresholds) honours the same
//     RLS partition as the natural cycle scan.
//
// Returns the count of fired invoices and any errors encountered. Per-sub
// errors do not abort the whole scan — they're collected and returned so
// the operator dashboard can flag stuck rows without losing throughput.
func (e *Engine) ScanThresholds(ctx context.Context, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	candidates, err := e.subs.ListWithThresholds(ctx, postgres.Livemode(ctx), batchSize)
	if err != nil {
		return 0, []error{fmt.Errorf("list candidates: %w", err)}
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	fired := 0
	var errsOut []error
	for _, sub := range candidates {
		didFire, err := e.scanOneThreshold(ctx, sub)
		if err != nil {
			slog.Error("threshold scan failed for subscription",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
				"error", err,
			)
			errsOut = append(errsOut, fmt.Errorf("subscription %s: %w", sub.ID, err))
			continue
		}
		if didFire {
			fired++
		}
	}

	if fired > 0 {
		slog.Info("threshold scan fired invoices", "count", fired)
	}
	return fired, errsOut
}

// ScanThresholdsForClock is the catchup-path counterpart to
// ScanThresholds. ADR-029 Phase 3: the wall-clock cron's
// ListWithThresholds excludes clock-pinned subs; this method is the
// disjoint per-clock entry point called by the catchup orchestrator
// after period generation but before charge retry.
//
// Per-sub scan logic is identical to the cron path — the only
// difference is the candidate-fetch scope. Errors collected per-sub,
// not aborted-on.
func (e *Engine) ScanThresholdsForClock(ctx context.Context, tenantID, clockID string, batchSize int) (int, []error) {
	if batchSize <= 0 {
		batchSize = 50
	}
	candidates, err := e.subs.ListWithThresholdsForClock(ctx, tenantID, clockID, batchSize)
	if err != nil {
		return 0, []error{fmt.Errorf("list threshold candidates for clock %s: %w", clockID, err)}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	fired := 0
	var errsOut []error
	for _, sub := range candidates {
		didFire, err := e.scanOneThreshold(ctx, sub)
		if err != nil {
			slog.Error("threshold scan failed for clock-pinned subscription",
				"subscription_id", sub.ID,
				"tenant_id", sub.TenantID,
				"clock_id", clockID,
				"error", err,
			)
			errsOut = append(errsOut, fmt.Errorf("subscription %s: %w", sub.ID, err))
			continue
		}
		if didFire {
			fired++
		}
	}
	if fired > 0 {
		slog.Info("threshold scan fired invoices on advance", "clock_id", clockID, "count", fired)
	}
	return fired, errsOut
}

// scanOneThreshold evaluates a single subscription. Builds running line items
// over the partial cycle [period_start, now), checks each configured cap,
// and — when crossed — fires the early finalize via fireThreshold.
func (e *Engine) scanOneThreshold(ctx context.Context, sub domain.Subscription) (bool, error) {
	if sub.BillingThresholds == nil {
		return false, nil
	}
	if sub.Status != domain.SubscriptionActive && sub.Status != domain.SubscriptionTrialing {
		return false, nil
	}
	if sub.CurrentBillingPeriodStart == nil || sub.CurrentBillingPeriodEnd == nil {
		return false, nil
	}
	// Trialing subs are skipped at the natural cycle level too — there's no
	// invoice to fire, so a threshold "cross" during trial would just emit a
	// zero-amount draft. Defer until status flips.
	if sub.Status == domain.SubscriptionTrialing {
		return false, nil
	}
	// pause_collection neuters the financial side; firing a threshold under
	// pause would emit a draft that can't be charged or dunned. Skip — the
	// scan resumes when collection does.
	if sub.PauseCollection != nil {
		return false, nil
	}

	now := e.effectiveNow(ctx, sub)
	periodStart := *sub.CurrentBillingPeriodStart
	if !now.After(periodStart) {
		return false, nil
	}

	eval, err := e.evaluateThresholds(ctx, sub, periodStart, now)
	if err != nil {
		return false, fmt.Errorf("evaluate: %w", err)
	}
	if !eval.CrossedAny {
		return false, nil
	}

	return e.fireThreshold(ctx, sub, eval, periodStart, now)
}

// evaluateThresholds computes the partial-cycle running totals for a sub and
// reports whether any configured cap has been crossed. Reuses
// previewWithWindow's per-item base-fee + per-(meter, rule) usage line
// computation so the scan and the cycle scan agree on what "running total"
// means — fed to the same priority+claim LATERAL JOIN as the natural cycle.
func (e *Engine) evaluateThresholds(ctx context.Context, sub domain.Subscription, periodStart, now time.Time) (thresholdEval, error) {
	preview, err := e.previewWithWindow(ctx, sub, periodStart, now)
	if err != nil {
		return thresholdEval{}, err
	}

	// in_advance base fees are billed up front by BillOnCreate / cycle close,
	// so they must NOT count toward the threshold running total or ride along
	// on the early-finalize invoice — doing so double-bills the prepaid base.
	// previewWithWindow emits one base_fee line per item (in sub.Items order)
	// whose plan has BaseAmountCents > 0; mirror that ordering to know each
	// base_fee preview line's timing. Predicate matches billOnePeriod's base
	// loop (engine.go base-segment skip). in_arrears base fees and all usage
	// lines pass through unchanged.
	inAdvanceBaseTiming := make([]bool, 0, len(sub.Items))
	for _, it := range sub.Items {
		plan, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return thresholdEval{}, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		if plan.BaseAmountCents <= 0 {
			continue
		}
		inAdvanceBaseTiming = append(inAdvanceBaseTiming, plan.BaseBillTiming == domain.BillInAdvance)
	}

	// Roll up the running subtotal across the previewed lines. Multi-currency
	// sub already filtered upstream at PATCH time, so we sum across all lines
	// regardless of per-line currency — there's only one.
	var running int64
	currency := ""
	baseFeeIdx := 0
	lineItems := make([]domain.InvoiceLineItem, 0, len(preview.Lines))
	for _, pl := range preview.Lines {
		if pl.LineType == "base_fee" {
			isInAdvance := baseFeeIdx < len(inAdvanceBaseTiming) && inAdvanceBaseTiming[baseFeeIdx]
			baseFeeIdx++
			if isInAdvance {
				// Already prepaid — skip from running total and line items.
				continue
			}
		}
		running += pl.AmountCents
		if currency == "" {
			currency = pl.Currency
		}
		var ps, pe *time.Time
		if pl.LineType == "usage" {
			s := periodStart
			n := now
			ps = &s
			pe = &n
		}
		lineType := domain.InvoiceLineItemType(pl.LineType)
		quantity := pl.Quantity.IntPart()
		// Usage lines carry the exact fractional quantity (cycle-writer
		// parity, engine.go billOnePeriod) so qty × unit reconciles with
		// the amount on display surfaces. Base-fee lines keep the zero
		// value per the QuantityDecimal contract ("use Quantity").
		var qtyDecimal decimal.Decimal
		if lineType == domain.LineTypeUsage {
			qtyDecimal = pl.Quantity
		}
		lineItems = append(lineItems, domain.InvoiceLineItem{
			LineType:            lineType,
			MeterID:             pl.MeterID,
			Description:         pl.Description,
			Quantity:            quantity,
			QuantityDecimal:     qtyDecimal,
			UnitAmountCents:     pl.UnitAmountCents,
			AmountCents:         pl.AmountCents,
			TotalAmountCents:    pl.AmountCents,
			Currency:            pl.Currency,
			PricingMode:         pl.PricingMode,
			RatingRuleVersionID: pl.RatingRuleVersionID,
			BillingPeriodStart:  ps,
			BillingPeriodEnd:    pe,
		})
	}

	eval := thresholdEval{
		RunningSubtotal: running,
		LineItems:       lineItems,
		InvoiceCurrency: currency,
	}

	// Amount cap: cycle subtotal in cents.
	bt := sub.BillingThresholds
	if bt.AmountGTE > 0 && running >= bt.AmountGTE {
		eval.CrossedAny = true
		eval.CrossedAmount = true
	}

	// Per-item caps: sum the running quantity across each item's plan
	// meters during [periodStart, now). The meter quantity is the same
	// figure the natural cycle would bill — pulled from the priority+claim
	// resolver via AggregateByPricingRules. Item-to-meter is via the plan
	// (item.plan_id -> plan.meter_ids). Cross-currency items already
	// rejected upstream, so summing across plans is safe.
	if !eval.CrossedAny && len(bt.ItemThresholds) > 0 {
		// Build a map from subscription_item_id -> running quantity sum.
		// The sum aggregates every meter on the item's plan.
		itemRunning := make(map[string]decimal.Decimal, len(sub.Items))
		for _, it := range sub.Items {
			plan, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
			if err != nil {
				// Plan resolution errors are surfaced but don't kill the whole
				// scan — fall through, the per-item check just won't fire on
				// this row. The cycle scan will surface the same error later
				// if the misconfig persists.
				slog.Warn("threshold scan: get plan failed",
					"subscription_id", sub.ID,
					"item_id", it.ID,
					"plan_id", it.PlanID,
					"error", err,
				)
				continue
			}
			total := decimal.Zero
			for _, meterID := range plan.MeterIDs {
				meter, mErr := e.pricing.GetMeter(ctx, sub.TenantID, meterID)
				if mErr != nil {
					continue
				}
				defaultMode := mapMeterAggregation(meter.Aggregation)
				aggs, aErr := e.usage.AggregateByPricingRules(ctx, sub.TenantID, sub.CustomerID, meterID, defaultMode, periodStart, now)
				if aErr != nil {
					continue
				}
				for _, agg := range aggs {
					total = total.Add(agg.Quantity)
				}
			}
			itemRunning[it.ID] = total
		}
		for _, t := range bt.ItemThresholds {
			running, ok := itemRunning[t.SubscriptionItemID]
			if !ok {
				continue
			}
			if running.GreaterThanOrEqual(t.UsageGTE) {
				eval.CrossedAny = true
				eval.CrossedItem = true
				eval.CrossedItemID = t.SubscriptionItemID
				break
			}
		}
	}

	return eval, nil
}

// fireThreshold emits the early-finalize invoice and (when reset_billing_cycle
// is true) advances the subscription's cycle as if the period had ended
// naturally. Returns (true, nil) on a successful fire, (false, nil) when a
// concurrent retry already produced the same invoice (idempotent skip).
//
// The invoice is built from the same line items evaluateThresholds already
// computed — re-aggregating would risk a different running total if usage
// landed between evaluate and fire. Tax + credit are applied via
// the same paths the natural cycle uses so an early-finalize charge looks
// identical to the customer.
func (e *Engine) fireThreshold(ctx context.Context, sub domain.Subscription, eval thresholdEval, periodStart, now time.Time) (bool, error) {
	if e.settings == nil {
		return false, fmt.Errorf("settings reader required for invoice numbering")
	}

	// Resolve the items' plan map for cycle-advance interval.
	plans := make(map[string]domain.Plan, len(sub.Items))
	for _, it := range sub.Items {
		if _, ok := plans[it.PlanID]; ok {
			continue
		}
		pl, err := e.pricing.GetPlan(ctx, sub.TenantID, it.PlanID)
		if err != nil {
			return false, fmt.Errorf("get plan %s: %w", it.PlanID, err)
		}
		plans[it.PlanID] = pl
	}

	invoiceCurrency := eval.InvoiceCurrency
	if invoiceCurrency == "" {
		// Empty preview lines (zero base fees, zero usage) would land here.
		// In that case the running subtotal is zero so we shouldn't have
		// reached the fire path — defensive bail.
		return false, fmt.Errorf("no invoice currency resolved for threshold fire")
	}
	if e.profiles != nil {
		if bp, err := e.profiles.GetBillingProfile(ctx, sub.TenantID, sub.CustomerID); err == nil && bp.Currency != "" {
			invoiceCurrency = bp.Currency
		}
	}

	// Coupons removed 2026-05-29 (Phase A1). Discount stays at zero;
	// discount intent flows through the credit ledger.
	subtotal := eval.RunningSubtotal
	var discountCents int64

	// Propagate tax errors rather than discarding them: a swallowed error
	// yields a zero-value TaxApplication (TaxStatus="") that finalizes a $0
	// threshold invoice and permanently consumes the cycle idempotency slot,
	// leaving the over-threshold usage unbilled. Mirrors the cycle-close and
	// proration call sites. (audit: velox-ops threshold tax-discard finding)
	taxApp, err := e.ApplyTaxToLineItems(ctx, sub.TenantID, sub.CustomerID, invoiceCurrency, subtotal, discountCents, eval.LineItems)
	if err != nil {
		return false, fmt.Errorf("apply tax: %w", err)
	}
	totalWithTax := taxApp.SubtotalCents - taxApp.DiscountCents + taxApp.TaxAmountCents

	netDays := 30
	if ts, err := e.settings.Get(ctx, sub.TenantID); err == nil && ts.NetPaymentTerms > 0 {
		netDays = ts.NetPaymentTerms
	}

	invoiceNumber, err := e.settings.NextInvoiceNumber(ctx, sub.TenantID)
	if err != nil {
		return false, fmt.Errorf("allocate invoice number: %w", err)
	}

	dueAt := now.AddDate(0, 0, netDays)

	// Shared finalization gate (tax-pending OR pause-collection → Draft),
	// same as every other invoice writer. The scan currently skips paused
	// subs upstream (scanOneThreshold), so the pause arm is unreachable
	// today — but a hand-rolled tax-only gate here was wrong-by-
	// construction: any relaxation of that skip would have finalized and
	// auto-charged a paused subscription.
	invStatus := domain.InvoiceFinalizationStatus(taxApp.TaxStatus, sub.PauseCollection)

	// Emit the threshold invoice. The partial unique index on
	// (tenant, sub, billing_period_start) WHERE billing_reason='threshold'
	// is the idempotency seam — a re-tick lands on errs.ErrAlreadyExists
	// and we short-circuit. Note: we use periodStart as the boundary key,
	// not now, so two ticks fired against the same in-flight cycle dedup
	// to the same row even though their wall-clock differs.
	inv, err := e.invoices.CreateInvoiceWithLineItems(ctx, sub.TenantID, domain.Invoice{
		CustomerID:       sub.CustomerID,
		SubscriptionID:   sub.ID,
		InvoiceNumber:    invoiceNumber,
		Status:           invStatus,
		PaymentStatus:    domain.PaymentPending,
		Currency:         invoiceCurrency,
		SubtotalCents:    taxApp.SubtotalCents,
		DiscountCents:    taxApp.DiscountCents,
		TaxRate:          taxApp.TaxRate,
		TaxName:          taxApp.TaxName,
		TaxCountry:       taxApp.TaxCountry,
		TaxID:            taxApp.TaxID,
		TaxAmountCents:   taxApp.TaxAmountCents,
		TaxProvider:      taxApp.TaxProvider,
		TaxCalculationID: taxApp.TaxCalculationID,
		TaxReverseCharge: taxApp.TaxReverseCharge,
		TaxExemptReason:  taxApp.TaxExemptReason,
		TaxStatus:        taxApp.TaxStatus,
		TaxDeferredAt:    taxApp.TaxDeferredAt,
		TaxPendingReason: taxApp.TaxPendingReason,
		// TaxErrorCode lets the dashboard banner + webhook consumers
		// branch on cause (fix-customer-data vs provider-outage). Was
		// the one TaxApplication field this writer dropped.
		TaxErrorCode:       taxApp.TaxErrorCode,
		TotalAmountCents:   totalWithTax,
		AmountDueCents:     totalWithTax,
		BillingPeriodStart: periodStart,
		BillingPeriodEnd:   now,
		IssuedAt:           &now,
		DueAt:              &dueAt,
		// CreatedAt on test-clock time — keeps engine-driven rows
		// internally consistent (Stripe / Lago / Orb pattern).
		CreatedAt:          now,
		NetPaymentTermDays: netDays,
		BillingReason:      domain.BillingReasonThreshold,
		// Threshold invoices fire on clock-pinned subs too (the
		// ScanThresholdsForClock catchup path); without this stamp the
		// invoice landed is_simulated=false and rendered without the
		// Simulated badge while sibling cycle/proration invoices on the
		// same clock showed it. Matches every other engine writer.
		IsSimulated: sub.TestClockID != "",
	}, eval.LineItems)
	if err != nil {
		if errors.Is(err, errs.ErrAlreadyExists) {
			slog.Info("threshold invoice already exists for cycle (idempotent skip)",
				"subscription_id", sub.ID,
				"period_start", periodStart,
			)
			return false, nil
		}
		return false, fmt.Errorf("create threshold invoice: %w", err)
	}

	// Commit tax (Stripe parity) — matches the cycle scan's flow.
	if inv.TaxProvider != "" && inv.TaxCalculationID != "" {
		if err := e.CommitTax(ctx, sub.TenantID, inv.ID, inv.TaxCalculationID); err != nil {
			slog.Warn("threshold scan: tax commit failed after invoice creation",
				"error", err, "tenant_id", sub.TenantID, "invoice_id", inv.ID)
		}
	}

	// Apply customer credits before charging. Same shape as the cycle scan.
	if e.credits != nil && totalWithTax > 0 {
		if _, err := e.credits.ApplyToInvoiceAt(ctx, sub.TenantID, sub.CustomerID, inv.ID, totalWithTax, now, inv.InvoiceNumber); err != nil {
			slog.Warn("threshold scan: failed to apply credits", "invoice_id", inv.ID, "error", err)
		}
	}

	// If credits covered 100%, mark as paid immediately — BUT only on
	// invoices that landed as finalized at create time. Draft invoices
	// (tax pending / pause-collection) stay draft with credits applied;
	// tax-retry / pause-resume's auto-finalize chains land the
	// transition later. Mirrors the same gate added to billOnePeriod's
	// equivalent block (2026-05-22 fix — invoice DEMO-000906).
	if totalWithTax > 0 && inv.Status == domain.InvoiceFinalized {
		updatedInv, err := e.invoices.GetInvoice(ctx, sub.TenantID, inv.ID)
		if err == nil && updatedInv.AmountDueCents <= 0 {
			if _, err := e.invoices.MarkPaid(ctx, sub.TenantID, inv.ID, "", now); err != nil {
				slog.Warn("threshold scan: failed to mark fully-credited invoice as paid",
					"invoice_id", inv.ID, "error", err)
			}
		}
	}

	// Auto-charge: synchronous with timeout, same behaviour as the cycle scan.
	if e.charger != nil && e.paymentSetups != nil && inv.AmountDueCents > 0 {
		if stripeCusID, hasDefaultPM, err := e.paymentSetups.ResolveForCharge(ctx, sub.TenantID, sub.CustomerID); err == nil &&
			hasDefaultPM && stripeCusID != "" {
			chargeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			chargeInv, err := e.invoices.GetInvoice(chargeCtx, sub.TenantID, inv.ID)
			if err == nil && chargeInv.AmountDueCents > 0 {
				if _, err := e.charger.ChargeInvoice(chargeCtx, sub.TenantID, chargeInv, stripeCusID); err != nil {
					_ = e.invoices.SetAutoChargePending(ctx, sub.TenantID, inv.ID, true)
				}
			}
		}
	}

	// Emit subscription.threshold_crossed before the optional cycle reset
	// so consumers see the crossing event ahead of any cycle-rollover side
	// effects.
	if e.events != nil {
		payload := map[string]any{
			"subscription_id":  sub.ID,
			"customer_id":      sub.CustomerID,
			"invoice_id":       inv.ID,
			"running_subtotal": eval.RunningSubtotal,
			"crossed_amount":   eval.CrossedAmount,
			"crossed_item_id":  eval.CrossedItemID,
			"reset_cycle":      sub.BillingThresholds.ResetBillingCycle,
		}
		if err := e.events.Dispatch(ctx, sub.TenantID, domain.EventSubscriptionThresholdCrossed, payload); err != nil {
			slog.Error("threshold scan: dispatch subscription.threshold_crossed failed",
				"subscription_id", sub.ID, "tenant_id", sub.TenantID, "error", err)
		}
	}

	// Cycle reset (when configured): the new cycle starts at fire time and
	// the next bill is the natural cycle invoice. When reset_billing_cycle
	// is false, the original cycle continues — a second invoice will fire
	// at the natural cycle end with whatever residual usage accumulated.
	if sub.BillingThresholds.ResetBillingCycle {
		nextPeriodStart := now
		nextPeriodEnd := advanceBillingPeriod(now, plans[sub.Items[0].PlanID].BillingInterval, e.tenantLocation(ctx, sub.TenantID))
		if err := e.subs.UpdateBillingCycle(ctx, sub.TenantID, sub.ID, nextPeriodStart, nextPeriodEnd, nextPeriodEnd); err != nil {
			// Cycle advance failure is non-fatal at the count level: the
			// invoice already exists, so we return fired=true and let the
			// next tick reconcile. The partial unique index ensures the
			// next tick won't double-fire.
			slog.Error("threshold scan: cycle advance failed after fire",
				"subscription_id", sub.ID,
				"invoice_id", inv.ID,
				"error", err,
			)
			return true, nil
		}
	}

	slog.Info("threshold-fired invoice generated",
		"invoice_id", inv.ID,
		"subscription_id", sub.ID,
		"running_subtotal", eval.RunningSubtotal,
		"crossed_amount", eval.CrossedAmount,
		"crossed_item_id", eval.CrossedItemID,
		"total_cents", totalWithTax,
		"reset_cycle", sub.BillingThresholds.ResetBillingCycle,
	)

	return true, nil
}
