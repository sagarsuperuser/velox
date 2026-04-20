// Command velox-backfill-email-bidx populates customers.email_bidx for
// rows that predate migration 0023. Operators run this once (per env,
// per key rotation) to make the magic-link email lookup find existing
// customers — new INSERTs populate the column automatically when the
// blinder is configured, but historical rows were created before the
// column existed and have NULL unconditionally.
//
// Running the tool is idempotent: it advances through customers by ID
// cursor, only touches rows where email_bidx IS NULL, and leaves
// already-populated indexes alone. Re-running after a partial run or a
// failure simply picks up where it left off. The tool uses TxBypass
// (superuser/migrate role) because the scan is cross-tenant by design
// — no single tenant context applies.
//
// Required env vars:
//
//	DATABASE_URL          — app DB URL (same as velox binary).
//	VELOX_EMAIL_BIDX_KEY  — 64-hex HMAC key. Running with a different
//	                        key than the webserver would produce blind
//	                        indexes that never match at request time.
//	VELOX_ENCRYPTION_KEY  — 64-hex AES key. Required if any row has
//	                        encrypted email (enc:-prefixed). Optional
//	                        if emails are plaintext — then the tool
//	                        skips any enc:-prefixed row instead of
//	                        producing garbage blind indexes.
//
// Exit codes: 0 on success (including zero rows updated), 1 on any
// configuration or fatal DB error. Per-row decryption failures are
// logged and skipped, not fatal — a single corrupt row shouldn't block
// the rest of the backfill.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sagarsuperuser/velox/internal/config"
	"github.com/sagarsuperuser/velox/internal/platform/crypto"
	"github.com/sagarsuperuser/velox/internal/platform/postgres"
)

// batchSize caps the rows loaded per iteration so an instance with
// millions of customers doesn't pin all of them in memory at once. A
// batch of 500 at ~1 KB/row fits comfortably on a small worker and
// still amortises tx overhead.
const batchSize = 500

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	bidxKey := strings.TrimSpace(os.Getenv("VELOX_EMAIL_BIDX_KEY"))
	if bidxKey == "" {
		fatal("VELOX_EMAIL_BIDX_KEY is required — the blind index key must match the webserver's key, or lookups will never match")
	}
	blinder, err := crypto.NewBlinder(bidxKey)
	if err != nil {
		fatal("VELOX_EMAIL_BIDX_KEY invalid: %v", err)
	}

	encKey := strings.TrimSpace(os.Getenv("VELOX_ENCRYPTION_KEY"))
	var enc *crypto.Encryptor
	if encKey != "" {
		e, err := crypto.NewEncryptor(encKey)
		if err != nil {
			fatal("VELOX_ENCRYPTION_KEY invalid: %v", err)
		}
		enc = e
	} else {
		// Plaintext-email deployments are legal (dev environments without
		// the key configured). crypto.Encryptor.Decrypt passes non-prefixed
		// values through, and errors on enc:-prefixed values with no key —
		// the per-row error handler logs + skips in that case.
		slog.Warn("VELOX_ENCRYPTION_KEY is empty — plaintext emails will be indexed; any enc:-prefixed row is unreadable and will be skipped")
		enc = crypto.NewNoop()
	}

	dbCfg, err := config.LoadDBOnly()
	if err != nil {
		fatal("load db config: %v", err)
	}
	pool, err := config.OpenPostgres(dbCfg)
	if err != nil {
		fatal("open db: %v", err)
	}
	defer func() { _ = pool.Close() }()

	db := postgres.NewDB(pool, 30*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	stats := backfillStats{startedAt: time.Now()}
	cursor := "" // monotonically advances so skip-only batches still make progress
	for {
		last, seen, err := backfillBatch(ctx, db, enc, blinder, cursor, &stats)
		if err != nil {
			fatal("backfill batch: %v", err)
		}
		if seen == 0 {
			break
		}
		cursor = last
	}

	slog.Info("backfill complete",
		"scanned", stats.scanned,
		"updated", stats.updated,
		"skipped_empty", stats.skippedEmpty,
		"skipped_decrypt_err", stats.skippedDecryptErr,
		"skipped_blind_empty", stats.skippedBlindEmpty,
		"duration", time.Since(stats.startedAt).Round(time.Millisecond).String(),
	)
}

// backfillStats collects per-run telemetry; reported once at exit so
// operators can sanity-check the size of the effect (e.g. "14 rows
// updated across 2M customers means everyone else already had the
// index, which is what I expected after the second run").
type backfillStats struct {
	startedAt         time.Time
	scanned           int
	updated           int
	skippedEmpty      int
	skippedDecryptErr int
	skippedBlindEmpty int
}

// backfillBatch loads up to batchSize customers with id > cursor that
// still have NULL email_bidx and a non-empty email, computes the blind
// index for each, and writes them back. Returns the id of the last row
// seen (for cursor advance) and the count scanned this batch. Zero
// scanned means the backfill has drained to the end of the table.
//
// Cursor-by-id ensures skip-only batches still make progress: even if
// every row in this batch fails to decrypt, `id > cursor` steps past
// them on the next call rather than looping forever on the same set.
//
// We read and write under TxBypass because the query is inherently
// cross-tenant. The row-level UPDATE is safe: we match by primary key,
// and set_livemode_from_session fires only on INSERT.
func backfillBatch(ctx context.Context, db *postgres.DB, enc *crypto.Encryptor, blinder *crypto.Blinder, cursor string, stats *backfillStats) (string, int, error) {
	tx, err := db.BeginTx(ctx, postgres.TxBypass, "")
	if err != nil {
		return "", 0, fmt.Errorf("begin: %w", err)
	}
	defer postgres.Rollback(tx)

	rows, err := tx.QueryContext(ctx, `
		SELECT id, email
		FROM customers
		WHERE id > $1
		  AND email_bidx IS NULL
		  AND email IS NOT NULL
		  AND email != ''
		ORDER BY id
		LIMIT $2
	`, cursor, batchSize)
	if err != nil {
		return "", 0, fmt.Errorf("select: %w", err)
	}

	type pending struct {
		id    string
		blind string
	}
	var updates []pending
	var lastID string
	seen := 0
	for rows.Next() {
		seen++
		stats.scanned++
		var id, emailCipher string
		if err := rows.Scan(&id, &emailCipher); err != nil {
			_ = rows.Close()
			return "", 0, fmt.Errorf("scan: %w", err)
		}
		lastID = id

		plain, err := enc.Decrypt(emailCipher)
		if err != nil {
			// Per-row failure: log and skip. A single corrupted row must
			// not block the rest of the backfill — the operator gets to
			// hand-audit the skipped IDs after the log aggregates.
			slog.Warn("decrypt failed, skipping customer",
				"customer_id", id, "error", err)
			stats.skippedDecryptErr++
			continue
		}
		plain = strings.ToLower(strings.TrimSpace(plain))
		if plain == "" {
			stats.skippedEmpty++
			continue
		}
		blind := blinder.Blind(plain)
		if blind == "" {
			// Shouldn't happen — blinder is guaranteed enabled here. Guard
			// against a degenerate future where Blind returns "" so we
			// leave NULL rather than poisoning the index with empty.
			stats.skippedBlindEmpty++
			continue
		}
		updates = append(updates, pending{id: id, blind: blind})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", 0, fmt.Errorf("rows: %w", err)
	}
	_ = rows.Close()

	for _, u := range updates {
		// AND email_bidx IS NULL keeps the UPDATE safe under a concurrent
		// writer that already populated the row — we never stomp a fresh
		// index with our (potentially stale) plaintext decode.
		if _, err := tx.ExecContext(ctx,
			`UPDATE customers SET email_bidx = $1 WHERE id = $2 AND email_bidx IS NULL`,
			u.blind, u.id,
		); err != nil {
			return "", 0, fmt.Errorf("update %s: %w", u.id, err)
		}
		stats.updated++
	}

	if err := tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("commit: %w", err)
	}
	return lastID, seen, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
