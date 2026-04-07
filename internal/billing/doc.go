// Package billing contains the billing cycle engine, scheduler, invoice
// preview, and MRR/ARR metrics. It orchestrates across domain boundaries
// (subscription, usage, pricing, invoice) via narrow interfaces without
// those domains knowing about each other.
package billing
