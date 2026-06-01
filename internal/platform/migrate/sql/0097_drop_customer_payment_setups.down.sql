-- Reverse 0097: recreate customer_payment_setups in the exact shape it had at
-- migration 0096 (the state immediately before 0097's up dropped it).
--
-- A no-op down (the original "SELECT 1") is NOT reversible: a full rollback
-- replays, in reverse order, the downs of every migration that built this
-- table while it still existed —
--   * 0021 (the set_livemode BEFORE INSERT trigger),
--   * 0020 (the livemode column + livemode-aware tenant_isolation RLS policy),
--   * 0015 (the explicit-RESTRICT foreign keys),
--   * 0001 (the final DROP TABLE).
-- Each of those fails with "relation customer_payment_setups does not exist"
-- once 0097 has removed the table and refuses to put it back. The same gap
-- breaks any partial down→up cycle whose forward leg re-runs the 0095/0096
-- backfills, which read this table (this is what reddened TestMigrationRoundTrip
-- and TestNoTxMigration_GINIndexBuilt). Restoring the structure here lets the
-- reverse chain unwind cleanly and matches the 0081/0082/0083 drop-migrations,
-- which all recreate their dropped table verbatim in the down direction.
--
-- This restores the empty SCHEMA only. By 0097 the data already lived in the
-- canonical sources (customers.stripe_customer_id + payment_methods); a rollback
-- that needs the rows back must re-derive them from those sources.

CREATE TABLE customer_payment_setups (
    customer_id                     TEXT NOT NULL,
    tenant_id                       TEXT NOT NULL,
    setup_status                    TEXT NOT NULL DEFAULT 'missing',
    default_payment_method_present  BOOLEAN NOT NULL DEFAULT false,
    payment_method_type             TEXT,
    stripe_customer_id              TEXT,
    stripe_payment_method_id        TEXT,
    last_verified_at                TIMESTAMPTZ,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT now(),
    card_brand                      TEXT,
    card_last4                      TEXT,
    card_exp_month                  INT,
    card_exp_year                   INT,
    livemode                        BOOLEAN NOT NULL DEFAULT true,
    CONSTRAINT customer_payment_setups_setup_status_check
        CHECK (setup_status IN ('missing', 'pending', 'ready', 'error')),
    CONSTRAINT customer_payment_setups_pkey PRIMARY KEY (tenant_id, customer_id),
    CONSTRAINT customer_payment_setups_customer_id_fkey
        FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE RESTRICT,
    CONSTRAINT customer_payment_setups_tenant_id_fkey
        FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT
);

ALTER TABLE customer_payment_setups ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_payment_setups FORCE ROW LEVEL SECURITY;

-- Livemode-aware tenant isolation, matching the policy 0020 installed. 0020's
-- own down rewrites this to the pre-test-mode form (and drops the livemode
-- column) before it runs, so the dependency on the livemode column unwinds in
-- the right order.
CREATE POLICY tenant_isolation ON customer_payment_setups USING (
    current_setting('app.bypass_rls', true) = 'on'
    OR (
        tenant_id = current_setting('app.tenant_id', true)
        AND livemode = (current_setting('app.livemode', true) IS DISTINCT FROM 'off')
    )
);

-- set_livemode_from_session() is still present at this point in the rollback
-- (0021's down, which drops it, runs after this one).
CREATE TRIGGER set_livemode
    BEFORE INSERT ON customer_payment_setups
    FOR EACH ROW EXECUTE FUNCTION set_livemode_from_session();
