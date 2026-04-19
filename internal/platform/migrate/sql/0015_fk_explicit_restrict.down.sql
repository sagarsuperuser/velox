-- Revert 0015: restore NO ACTION (the PostgreSQL default) on every FK that
-- 0015 made explicit RESTRICT. Behaviorally equivalent for non-deferrable FKs
-- but brings the schema back to its pre-0015 form.

ALTER TABLE api_keys
    DROP CONSTRAINT api_keys_tenant_id_fkey,
    ADD CONSTRAINT api_keys_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE audit_log
    DROP CONSTRAINT audit_log_tenant_id_fkey,
    ADD CONSTRAINT audit_log_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE billed_entries
    DROP CONSTRAINT billed_entries_customer_id_fkey,
    ADD CONSTRAINT billed_entries_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT billed_entries_meter_id_fkey,
    ADD CONSTRAINT billed_entries_meter_id_fkey FOREIGN KEY (meter_id) REFERENCES meters(id),
    DROP CONSTRAINT billed_entries_tenant_id_fkey,
    ADD CONSTRAINT billed_entries_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE billing_provider_connections
    DROP CONSTRAINT billing_provider_connections_tenant_id_fkey,
    ADD CONSTRAINT billing_provider_connections_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE coupon_redemptions
    DROP CONSTRAINT coupon_redemptions_coupon_id_fkey,
    ADD CONSTRAINT coupon_redemptions_coupon_id_fkey FOREIGN KEY (coupon_id) REFERENCES coupons(id),
    DROP CONSTRAINT coupon_redemptions_tenant_id_fkey,
    ADD CONSTRAINT coupon_redemptions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE coupons
    DROP CONSTRAINT coupons_tenant_id_fkey,
    ADD CONSTRAINT coupons_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE credit_note_line_items
    DROP CONSTRAINT credit_note_line_items_credit_note_id_fkey,
    ADD CONSTRAINT credit_note_line_items_credit_note_id_fkey FOREIGN KEY (credit_note_id) REFERENCES credit_notes(id),
    DROP CONSTRAINT credit_note_line_items_tenant_id_fkey,
    ADD CONSTRAINT credit_note_line_items_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE credit_notes
    DROP CONSTRAINT credit_notes_customer_id_fkey,
    ADD CONSTRAINT credit_notes_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT credit_notes_invoice_id_fkey,
    ADD CONSTRAINT credit_notes_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id),
    DROP CONSTRAINT credit_notes_tenant_id_fkey,
    ADD CONSTRAINT credit_notes_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customer_billing_profiles
    DROP CONSTRAINT customer_billing_profiles_customer_id_fkey,
    ADD CONSTRAINT customer_billing_profiles_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT customer_billing_profiles_tenant_id_fkey,
    ADD CONSTRAINT customer_billing_profiles_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customer_credit_ledger
    DROP CONSTRAINT customer_credit_ledger_customer_id_fkey,
    ADD CONSTRAINT customer_credit_ledger_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT customer_credit_ledger_tenant_id_fkey,
    ADD CONSTRAINT customer_credit_ledger_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customer_dunning_overrides
    DROP CONSTRAINT customer_dunning_overrides_customer_id_fkey,
    ADD CONSTRAINT customer_dunning_overrides_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT customer_dunning_overrides_tenant_id_fkey,
    ADD CONSTRAINT customer_dunning_overrides_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customer_payment_setups
    DROP CONSTRAINT customer_payment_setups_customer_id_fkey,
    ADD CONSTRAINT customer_payment_setups_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT customer_payment_setups_tenant_id_fkey,
    ADD CONSTRAINT customer_payment_setups_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customer_price_overrides
    DROP CONSTRAINT customer_price_overrides_customer_id_fkey,
    ADD CONSTRAINT customer_price_overrides_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT customer_price_overrides_rating_rule_version_id_fkey,
    ADD CONSTRAINT customer_price_overrides_rating_rule_version_id_fkey FOREIGN KEY (rating_rule_version_id) REFERENCES rating_rule_versions(id),
    DROP CONSTRAINT customer_price_overrides_tenant_id_fkey,
    ADD CONSTRAINT customer_price_overrides_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE customers
    DROP CONSTRAINT customers_tenant_id_fkey,
    ADD CONSTRAINT customers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE dunning_policies
    DROP CONSTRAINT dunning_policies_tenant_id_fkey,
    ADD CONSTRAINT dunning_policies_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE invoice_dunning_events
    DROP CONSTRAINT invoice_dunning_events_invoice_id_fkey,
    ADD CONSTRAINT invoice_dunning_events_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id),
    DROP CONSTRAINT invoice_dunning_events_run_id_fkey,
    ADD CONSTRAINT invoice_dunning_events_run_id_fkey FOREIGN KEY (run_id) REFERENCES invoice_dunning_runs(id),
    DROP CONSTRAINT invoice_dunning_events_tenant_id_fkey,
    ADD CONSTRAINT invoice_dunning_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE invoice_dunning_runs
    DROP CONSTRAINT invoice_dunning_runs_invoice_id_fkey,
    ADD CONSTRAINT invoice_dunning_runs_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id),
    DROP CONSTRAINT invoice_dunning_runs_policy_id_fkey,
    ADD CONSTRAINT invoice_dunning_runs_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES dunning_policies(id),
    DROP CONSTRAINT invoice_dunning_runs_tenant_id_fkey,
    ADD CONSTRAINT invoice_dunning_runs_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE invoice_line_items
    DROP CONSTRAINT invoice_line_items_invoice_id_fkey,
    ADD CONSTRAINT invoice_line_items_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id),
    DROP CONSTRAINT invoice_line_items_tenant_id_fkey,
    ADD CONSTRAINT invoice_line_items_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE invoices
    DROP CONSTRAINT invoices_customer_id_fkey,
    ADD CONSTRAINT invoices_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT invoices_subscription_id_fkey,
    ADD CONSTRAINT invoices_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id),
    DROP CONSTRAINT invoices_tenant_id_fkey,
    ADD CONSTRAINT invoices_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE meters
    DROP CONSTRAINT meters_rating_rule_version_id_fkey,
    ADD CONSTRAINT meters_rating_rule_version_id_fkey FOREIGN KEY (rating_rule_version_id) REFERENCES rating_rule_versions(id),
    DROP CONSTRAINT meters_tenant_id_fkey,
    ADD CONSTRAINT meters_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE payment_update_tokens
    DROP CONSTRAINT payment_update_tokens_customer_id_fkey,
    ADD CONSTRAINT payment_update_tokens_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT payment_update_tokens_invoice_id_fkey,
    ADD CONSTRAINT payment_update_tokens_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id),
    DROP CONSTRAINT payment_update_tokens_tenant_id_fkey,
    ADD CONSTRAINT payment_update_tokens_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE plans
    DROP CONSTRAINT plans_tenant_id_fkey,
    ADD CONSTRAINT plans_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE rating_rule_versions
    DROP CONSTRAINT rating_rule_versions_tenant_id_fkey,
    ADD CONSTRAINT rating_rule_versions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE stripe_webhook_events
    DROP CONSTRAINT stripe_webhook_events_tenant_id_fkey,
    ADD CONSTRAINT stripe_webhook_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE subscriptions
    DROP CONSTRAINT subscriptions_customer_id_fkey,
    ADD CONSTRAINT subscriptions_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT subscriptions_plan_id_fkey,
    ADD CONSTRAINT subscriptions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id),
    DROP CONSTRAINT subscriptions_pending_plan_id_fkey,
    ADD CONSTRAINT subscriptions_pending_plan_id_fkey FOREIGN KEY (pending_plan_id) REFERENCES plans(id),
    DROP CONSTRAINT subscriptions_tenant_id_fkey,
    ADD CONSTRAINT subscriptions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE tenant_settings
    DROP CONSTRAINT tenant_settings_tenant_id_fkey,
    ADD CONSTRAINT tenant_settings_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE usage_events
    DROP CONSTRAINT usage_events_customer_id_fkey,
    ADD CONSTRAINT usage_events_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id),
    DROP CONSTRAINT usage_events_meter_id_fkey,
    ADD CONSTRAINT usage_events_meter_id_fkey FOREIGN KEY (meter_id) REFERENCES meters(id),
    DROP CONSTRAINT usage_events_subscription_id_fkey,
    ADD CONSTRAINT usage_events_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id),
    DROP CONSTRAINT usage_events_tenant_id_fkey,
    ADD CONSTRAINT usage_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE webhook_deliveries
    DROP CONSTRAINT webhook_deliveries_tenant_id_fkey,
    ADD CONSTRAINT webhook_deliveries_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id),
    DROP CONSTRAINT webhook_deliveries_webhook_endpoint_id_fkey,
    ADD CONSTRAINT webhook_deliveries_webhook_endpoint_id_fkey FOREIGN KEY (webhook_endpoint_id) REFERENCES webhook_endpoints(id),
    DROP CONSTRAINT webhook_deliveries_webhook_event_id_fkey,
    ADD CONSTRAINT webhook_deliveries_webhook_event_id_fkey FOREIGN KEY (webhook_event_id) REFERENCES webhook_events(id);

ALTER TABLE webhook_endpoints
    DROP CONSTRAINT webhook_endpoints_tenant_id_fkey,
    ADD CONSTRAINT webhook_endpoints_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE webhook_events
    DROP CONSTRAINT webhook_events_tenant_id_fkey,
    ADD CONSTRAINT webhook_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id);
