-- Revert 0015: restore NO ACTION (the PostgreSQL default) on every FK that
-- 0015 made explicit RESTRICT. Behaviorally equivalent for non-deferrable FKs
-- but brings the schema back to its pre-0015 form.
--
-- Same lock concern as the up direction: re-adding the FK without NOT VALID
-- validates every existing row under AccessExclusiveLock (~6.7s on audit_log
-- at the medium scale per docs/migration-safety-findings.md). Use the same
-- NOT VALID + VALIDATE two-step so rollbacks don't freeze writes either.

ALTER TABLE api_keys DROP CONSTRAINT api_keys_tenant_id_fkey;
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE api_keys VALIDATE CONSTRAINT api_keys_tenant_id_fkey;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_tenant_id_fkey;
ALTER TABLE audit_log
    ADD CONSTRAINT audit_log_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_tenant_id_fkey;

ALTER TABLE billed_entries DROP CONSTRAINT billed_entries_customer_id_fkey;
ALTER TABLE billed_entries
    ADD CONSTRAINT billed_entries_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE billed_entries VALIDATE CONSTRAINT billed_entries_customer_id_fkey;

ALTER TABLE billed_entries DROP CONSTRAINT billed_entries_meter_id_fkey;
ALTER TABLE billed_entries
    ADD CONSTRAINT billed_entries_meter_id_fkey FOREIGN KEY (meter_id) REFERENCES meters(id) NOT VALID;
ALTER TABLE billed_entries VALIDATE CONSTRAINT billed_entries_meter_id_fkey;

ALTER TABLE billed_entries DROP CONSTRAINT billed_entries_tenant_id_fkey;
ALTER TABLE billed_entries
    ADD CONSTRAINT billed_entries_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE billed_entries VALIDATE CONSTRAINT billed_entries_tenant_id_fkey;

ALTER TABLE billing_provider_connections DROP CONSTRAINT billing_provider_connections_tenant_id_fkey;
ALTER TABLE billing_provider_connections
    ADD CONSTRAINT billing_provider_connections_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE billing_provider_connections VALIDATE CONSTRAINT billing_provider_connections_tenant_id_fkey;

ALTER TABLE coupon_redemptions DROP CONSTRAINT coupon_redemptions_coupon_id_fkey;
ALTER TABLE coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_coupon_id_fkey FOREIGN KEY (coupon_id) REFERENCES coupons(id) NOT VALID;
ALTER TABLE coupon_redemptions VALIDATE CONSTRAINT coupon_redemptions_coupon_id_fkey;

ALTER TABLE coupon_redemptions DROP CONSTRAINT coupon_redemptions_tenant_id_fkey;
ALTER TABLE coupon_redemptions
    ADD CONSTRAINT coupon_redemptions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE coupon_redemptions VALIDATE CONSTRAINT coupon_redemptions_tenant_id_fkey;

ALTER TABLE coupons DROP CONSTRAINT coupons_tenant_id_fkey;
ALTER TABLE coupons
    ADD CONSTRAINT coupons_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE coupons VALIDATE CONSTRAINT coupons_tenant_id_fkey;

ALTER TABLE credit_note_line_items DROP CONSTRAINT credit_note_line_items_credit_note_id_fkey;
ALTER TABLE credit_note_line_items
    ADD CONSTRAINT credit_note_line_items_credit_note_id_fkey FOREIGN KEY (credit_note_id) REFERENCES credit_notes(id) NOT VALID;
ALTER TABLE credit_note_line_items VALIDATE CONSTRAINT credit_note_line_items_credit_note_id_fkey;

ALTER TABLE credit_note_line_items DROP CONSTRAINT credit_note_line_items_tenant_id_fkey;
ALTER TABLE credit_note_line_items
    ADD CONSTRAINT credit_note_line_items_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE credit_note_line_items VALIDATE CONSTRAINT credit_note_line_items_tenant_id_fkey;

ALTER TABLE credit_notes DROP CONSTRAINT credit_notes_customer_id_fkey;
ALTER TABLE credit_notes
    ADD CONSTRAINT credit_notes_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE credit_notes VALIDATE CONSTRAINT credit_notes_customer_id_fkey;

ALTER TABLE credit_notes DROP CONSTRAINT credit_notes_invoice_id_fkey;
ALTER TABLE credit_notes
    ADD CONSTRAINT credit_notes_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id) NOT VALID;
ALTER TABLE credit_notes VALIDATE CONSTRAINT credit_notes_invoice_id_fkey;

ALTER TABLE credit_notes DROP CONSTRAINT credit_notes_tenant_id_fkey;
ALTER TABLE credit_notes
    ADD CONSTRAINT credit_notes_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE credit_notes VALIDATE CONSTRAINT credit_notes_tenant_id_fkey;

ALTER TABLE customer_billing_profiles DROP CONSTRAINT customer_billing_profiles_customer_id_fkey;
ALTER TABLE customer_billing_profiles
    ADD CONSTRAINT customer_billing_profiles_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE customer_billing_profiles VALIDATE CONSTRAINT customer_billing_profiles_customer_id_fkey;

ALTER TABLE customer_billing_profiles DROP CONSTRAINT customer_billing_profiles_tenant_id_fkey;
ALTER TABLE customer_billing_profiles
    ADD CONSTRAINT customer_billing_profiles_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customer_billing_profiles VALIDATE CONSTRAINT customer_billing_profiles_tenant_id_fkey;

ALTER TABLE customer_credit_ledger DROP CONSTRAINT customer_credit_ledger_customer_id_fkey;
ALTER TABLE customer_credit_ledger
    ADD CONSTRAINT customer_credit_ledger_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE customer_credit_ledger VALIDATE CONSTRAINT customer_credit_ledger_customer_id_fkey;

ALTER TABLE customer_credit_ledger DROP CONSTRAINT customer_credit_ledger_tenant_id_fkey;
ALTER TABLE customer_credit_ledger
    ADD CONSTRAINT customer_credit_ledger_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customer_credit_ledger VALIDATE CONSTRAINT customer_credit_ledger_tenant_id_fkey;

ALTER TABLE customer_dunning_overrides DROP CONSTRAINT customer_dunning_overrides_customer_id_fkey;
ALTER TABLE customer_dunning_overrides
    ADD CONSTRAINT customer_dunning_overrides_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE customer_dunning_overrides VALIDATE CONSTRAINT customer_dunning_overrides_customer_id_fkey;

ALTER TABLE customer_dunning_overrides DROP CONSTRAINT customer_dunning_overrides_tenant_id_fkey;
ALTER TABLE customer_dunning_overrides
    ADD CONSTRAINT customer_dunning_overrides_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customer_dunning_overrides VALIDATE CONSTRAINT customer_dunning_overrides_tenant_id_fkey;

ALTER TABLE customer_payment_setups DROP CONSTRAINT customer_payment_setups_customer_id_fkey;
ALTER TABLE customer_payment_setups
    ADD CONSTRAINT customer_payment_setups_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE customer_payment_setups VALIDATE CONSTRAINT customer_payment_setups_customer_id_fkey;

ALTER TABLE customer_payment_setups DROP CONSTRAINT customer_payment_setups_tenant_id_fkey;
ALTER TABLE customer_payment_setups
    ADD CONSTRAINT customer_payment_setups_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customer_payment_setups VALIDATE CONSTRAINT customer_payment_setups_tenant_id_fkey;

ALTER TABLE customer_price_overrides DROP CONSTRAINT customer_price_overrides_customer_id_fkey;
ALTER TABLE customer_price_overrides
    ADD CONSTRAINT customer_price_overrides_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE customer_price_overrides VALIDATE CONSTRAINT customer_price_overrides_customer_id_fkey;

ALTER TABLE customer_price_overrides DROP CONSTRAINT customer_price_overrides_rating_rule_version_id_fkey;
ALTER TABLE customer_price_overrides
    ADD CONSTRAINT customer_price_overrides_rating_rule_version_id_fkey FOREIGN KEY (rating_rule_version_id) REFERENCES rating_rule_versions(id) NOT VALID;
ALTER TABLE customer_price_overrides VALIDATE CONSTRAINT customer_price_overrides_rating_rule_version_id_fkey;

ALTER TABLE customer_price_overrides DROP CONSTRAINT customer_price_overrides_tenant_id_fkey;
ALTER TABLE customer_price_overrides
    ADD CONSTRAINT customer_price_overrides_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customer_price_overrides VALIDATE CONSTRAINT customer_price_overrides_tenant_id_fkey;

ALTER TABLE customers DROP CONSTRAINT customers_tenant_id_fkey;
ALTER TABLE customers
    ADD CONSTRAINT customers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE customers VALIDATE CONSTRAINT customers_tenant_id_fkey;

ALTER TABLE dunning_policies DROP CONSTRAINT dunning_policies_tenant_id_fkey;
ALTER TABLE dunning_policies
    ADD CONSTRAINT dunning_policies_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE dunning_policies VALIDATE CONSTRAINT dunning_policies_tenant_id_fkey;

ALTER TABLE invoice_dunning_events DROP CONSTRAINT invoice_dunning_events_invoice_id_fkey;
ALTER TABLE invoice_dunning_events
    ADD CONSTRAINT invoice_dunning_events_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id) NOT VALID;
ALTER TABLE invoice_dunning_events VALIDATE CONSTRAINT invoice_dunning_events_invoice_id_fkey;

ALTER TABLE invoice_dunning_events DROP CONSTRAINT invoice_dunning_events_run_id_fkey;
ALTER TABLE invoice_dunning_events
    ADD CONSTRAINT invoice_dunning_events_run_id_fkey FOREIGN KEY (run_id) REFERENCES invoice_dunning_runs(id) NOT VALID;
ALTER TABLE invoice_dunning_events VALIDATE CONSTRAINT invoice_dunning_events_run_id_fkey;

ALTER TABLE invoice_dunning_events DROP CONSTRAINT invoice_dunning_events_tenant_id_fkey;
ALTER TABLE invoice_dunning_events
    ADD CONSTRAINT invoice_dunning_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE invoice_dunning_events VALIDATE CONSTRAINT invoice_dunning_events_tenant_id_fkey;

ALTER TABLE invoice_dunning_runs DROP CONSTRAINT invoice_dunning_runs_invoice_id_fkey;
ALTER TABLE invoice_dunning_runs
    ADD CONSTRAINT invoice_dunning_runs_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id) NOT VALID;
ALTER TABLE invoice_dunning_runs VALIDATE CONSTRAINT invoice_dunning_runs_invoice_id_fkey;

ALTER TABLE invoice_dunning_runs DROP CONSTRAINT invoice_dunning_runs_policy_id_fkey;
ALTER TABLE invoice_dunning_runs
    ADD CONSTRAINT invoice_dunning_runs_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES dunning_policies(id) NOT VALID;
ALTER TABLE invoice_dunning_runs VALIDATE CONSTRAINT invoice_dunning_runs_policy_id_fkey;

ALTER TABLE invoice_dunning_runs DROP CONSTRAINT invoice_dunning_runs_tenant_id_fkey;
ALTER TABLE invoice_dunning_runs
    ADD CONSTRAINT invoice_dunning_runs_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE invoice_dunning_runs VALIDATE CONSTRAINT invoice_dunning_runs_tenant_id_fkey;

ALTER TABLE invoice_line_items DROP CONSTRAINT invoice_line_items_invoice_id_fkey;
ALTER TABLE invoice_line_items
    ADD CONSTRAINT invoice_line_items_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id) NOT VALID;
ALTER TABLE invoice_line_items VALIDATE CONSTRAINT invoice_line_items_invoice_id_fkey;

ALTER TABLE invoice_line_items DROP CONSTRAINT invoice_line_items_tenant_id_fkey;
ALTER TABLE invoice_line_items
    ADD CONSTRAINT invoice_line_items_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE invoice_line_items VALIDATE CONSTRAINT invoice_line_items_tenant_id_fkey;

ALTER TABLE invoices DROP CONSTRAINT invoices_customer_id_fkey;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE invoices VALIDATE CONSTRAINT invoices_customer_id_fkey;

ALTER TABLE invoices DROP CONSTRAINT invoices_subscription_id_fkey;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) NOT VALID;
ALTER TABLE invoices VALIDATE CONSTRAINT invoices_subscription_id_fkey;

ALTER TABLE invoices DROP CONSTRAINT invoices_tenant_id_fkey;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE invoices VALIDATE CONSTRAINT invoices_tenant_id_fkey;

ALTER TABLE meters DROP CONSTRAINT meters_rating_rule_version_id_fkey;
ALTER TABLE meters
    ADD CONSTRAINT meters_rating_rule_version_id_fkey FOREIGN KEY (rating_rule_version_id) REFERENCES rating_rule_versions(id) NOT VALID;
ALTER TABLE meters VALIDATE CONSTRAINT meters_rating_rule_version_id_fkey;

ALTER TABLE meters DROP CONSTRAINT meters_tenant_id_fkey;
ALTER TABLE meters
    ADD CONSTRAINT meters_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE meters VALIDATE CONSTRAINT meters_tenant_id_fkey;

ALTER TABLE payment_update_tokens DROP CONSTRAINT payment_update_tokens_customer_id_fkey;
ALTER TABLE payment_update_tokens
    ADD CONSTRAINT payment_update_tokens_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE payment_update_tokens VALIDATE CONSTRAINT payment_update_tokens_customer_id_fkey;

ALTER TABLE payment_update_tokens DROP CONSTRAINT payment_update_tokens_invoice_id_fkey;
ALTER TABLE payment_update_tokens
    ADD CONSTRAINT payment_update_tokens_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES invoices(id) NOT VALID;
ALTER TABLE payment_update_tokens VALIDATE CONSTRAINT payment_update_tokens_invoice_id_fkey;

ALTER TABLE payment_update_tokens DROP CONSTRAINT payment_update_tokens_tenant_id_fkey;
ALTER TABLE payment_update_tokens
    ADD CONSTRAINT payment_update_tokens_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE payment_update_tokens VALIDATE CONSTRAINT payment_update_tokens_tenant_id_fkey;

ALTER TABLE plans DROP CONSTRAINT plans_tenant_id_fkey;
ALTER TABLE plans
    ADD CONSTRAINT plans_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE plans VALIDATE CONSTRAINT plans_tenant_id_fkey;

ALTER TABLE rating_rule_versions DROP CONSTRAINT rating_rule_versions_tenant_id_fkey;
ALTER TABLE rating_rule_versions
    ADD CONSTRAINT rating_rule_versions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE rating_rule_versions VALIDATE CONSTRAINT rating_rule_versions_tenant_id_fkey;

ALTER TABLE stripe_webhook_events DROP CONSTRAINT stripe_webhook_events_tenant_id_fkey;
ALTER TABLE stripe_webhook_events
    ADD CONSTRAINT stripe_webhook_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE stripe_webhook_events VALIDATE CONSTRAINT stripe_webhook_events_tenant_id_fkey;

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_customer_id_fkey;
ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE subscriptions VALIDATE CONSTRAINT subscriptions_customer_id_fkey;

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_plan_id_fkey;
ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES plans(id) NOT VALID;
ALTER TABLE subscriptions VALIDATE CONSTRAINT subscriptions_plan_id_fkey;

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_pending_plan_id_fkey;
ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_pending_plan_id_fkey FOREIGN KEY (pending_plan_id) REFERENCES plans(id) NOT VALID;
ALTER TABLE subscriptions VALIDATE CONSTRAINT subscriptions_pending_plan_id_fkey;

ALTER TABLE subscriptions DROP CONSTRAINT subscriptions_tenant_id_fkey;
ALTER TABLE subscriptions
    ADD CONSTRAINT subscriptions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE subscriptions VALIDATE CONSTRAINT subscriptions_tenant_id_fkey;

ALTER TABLE tenant_settings DROP CONSTRAINT tenant_settings_tenant_id_fkey;
ALTER TABLE tenant_settings
    ADD CONSTRAINT tenant_settings_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE tenant_settings VALIDATE CONSTRAINT tenant_settings_tenant_id_fkey;

ALTER TABLE usage_events DROP CONSTRAINT usage_events_customer_id_fkey;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers(id) NOT VALID;
ALTER TABLE usage_events VALIDATE CONSTRAINT usage_events_customer_id_fkey;

ALTER TABLE usage_events DROP CONSTRAINT usage_events_meter_id_fkey;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_meter_id_fkey FOREIGN KEY (meter_id) REFERENCES meters(id) NOT VALID;
ALTER TABLE usage_events VALIDATE CONSTRAINT usage_events_meter_id_fkey;

ALTER TABLE usage_events DROP CONSTRAINT usage_events_subscription_id_fkey;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES subscriptions(id) NOT VALID;
ALTER TABLE usage_events VALIDATE CONSTRAINT usage_events_subscription_id_fkey;

ALTER TABLE usage_events DROP CONSTRAINT usage_events_tenant_id_fkey;
ALTER TABLE usage_events
    ADD CONSTRAINT usage_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE usage_events VALIDATE CONSTRAINT usage_events_tenant_id_fkey;

ALTER TABLE webhook_deliveries DROP CONSTRAINT webhook_deliveries_tenant_id_fkey;
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE webhook_deliveries VALIDATE CONSTRAINT webhook_deliveries_tenant_id_fkey;

ALTER TABLE webhook_deliveries DROP CONSTRAINT webhook_deliveries_webhook_endpoint_id_fkey;
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_webhook_endpoint_id_fkey FOREIGN KEY (webhook_endpoint_id) REFERENCES webhook_endpoints(id) NOT VALID;
ALTER TABLE webhook_deliveries VALIDATE CONSTRAINT webhook_deliveries_webhook_endpoint_id_fkey;

ALTER TABLE webhook_deliveries DROP CONSTRAINT webhook_deliveries_webhook_event_id_fkey;
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_webhook_event_id_fkey FOREIGN KEY (webhook_event_id) REFERENCES webhook_events(id) NOT VALID;
ALTER TABLE webhook_deliveries VALIDATE CONSTRAINT webhook_deliveries_webhook_event_id_fkey;

ALTER TABLE webhook_endpoints DROP CONSTRAINT webhook_endpoints_tenant_id_fkey;
ALTER TABLE webhook_endpoints
    ADD CONSTRAINT webhook_endpoints_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE webhook_endpoints VALIDATE CONSTRAINT webhook_endpoints_tenant_id_fkey;

ALTER TABLE webhook_events DROP CONSTRAINT webhook_events_tenant_id_fkey;
ALTER TABLE webhook_events
    ADD CONSTRAINT webhook_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) NOT VALID;
ALTER TABLE webhook_events VALIDATE CONSTRAINT webhook_events_tenant_id_fkey;
