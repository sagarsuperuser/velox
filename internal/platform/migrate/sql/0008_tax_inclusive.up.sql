-- When true, interpret tenant prices as tax-inclusive: the sticker price on a
-- plan is the customer-facing gross and the engine carves tax out of it at
-- invoice time. Default false preserves the existing exclusive-pricing contract
-- (tax added on top).
ALTER TABLE tenant_settings
    ADD COLUMN tax_inclusive BOOLEAN NOT NULL DEFAULT FALSE;
