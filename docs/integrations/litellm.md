# LiteLLM → Velox integration

Drop a `generic_api` callback in your LiteLLM proxy config and every LLM call lands in Velox as a usage event. No glue code.

Design: [ADR-033](../adr/033-litellm-spend-adapter.md).

## 1. Create the token meters in Velox

The adapter writes to two meters: `tokens_input` and `tokens_output`. Both must exist before LiteLLM starts POSTing.

Recommended: instantiate one of the AI-native recipes — both create the meters with sane aggregation + pricing-rule defaults.

```bash
curl -X POST "$VELOX/v1/recipes/anthropic_style/instantiate" \
  -H "Authorization: Bearer $VELOX_KEY" \
  -H "Content-Type: application/json" \
  -d '{"livemode": false}'
```

Or create them by hand:

```bash
curl -X POST "$VELOX/v1/meters" \
  -H "Authorization: Bearer $VELOX_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key":"tokens_input","name":"Tokens — input","unit":"tokens","aggregation":"sum"}'

curl -X POST "$VELOX/v1/meters" \
  -H "Authorization: Bearer $VELOX_KEY" \
  -H "Content-Type: application/json" \
  -d '{"key":"tokens_output","name":"Tokens — output","unit":"tokens","aggregation":"sum"}'
```

## 2. Configure the LiteLLM proxy

Add to your `litellm_config.yaml`:

```yaml
litellm_settings:
  success_callback: ["generic"]
  failure_callback: ["generic"]

environment_variables:
  GENERIC_LOGGER_ENDPOINT: "https://api.velox.dev/v1/integrations/litellm/spend"
  GENERIC_LOGGER_HEADERS: "Authorization=Bearer vlx_secret_test_…"
```

Replace `api.velox.dev` with your Velox API host (self-hosted: your own domain). Use a **secret** key — publishable keys don't have `PermUsageWrite`.

## 3. Set `user=` on every call

The adapter resolves LiteLLM's `user` field to a Velox customer via `external_id`. Set it on every LiteLLM call:

```python
import litellm

response = litellm.completion(
    model="claude-3-5-sonnet-20241022",
    messages=[{"role": "user", "content": "Hello"}],
    user="cus_acme_corp",   # ← Velox external_customer_id
    metadata={
        "team_id": "team_engineering",  # surfaces as a Velox dimension
    },
)
```

Without `user=`, the adapter rejects the event with `payload.user is required`. The rest of the batch is accepted normally (per-row 422, not full-batch 5xx).

## 4. What lands in Velox

For each completion call, the adapter emits up to two usage events:

| Event             | Quantity                  | Dimensions                                   | Idempotency key            |
|-------------------|---------------------------|----------------------------------------------|----------------------------|
| `tokens_input`    | `usage.prompt_tokens`     | `{model, provider, team_id?, request_tags?}` | `<litellm_id>:input`       |
| `tokens_output`   | `usage.completion_tokens` | same                                         | `<litellm_id>:output`      |

Each event's metadata carries the LiteLLM call ID, response cost (audit-only), and the call's original metadata under `litellm_metadata.*`.

## 5. Verify

```bash
# Tail recent usage events for a customer
curl "$VELOX/v1/usage-events?external_customer_id=cus_acme_corp&limit=5" \
  -H "Authorization: Bearer $VELOX_KEY"
```

You should see one `tokens_input` and one `tokens_output` event per LiteLLM call, with `model` / `provider` dimensions set.

## Reference: response shape

`POST /v1/integrations/litellm/spend` returns 200 with:

```json
{
  "accepted": 12,
  "skipped": 1,
  "errors": [
    {
      "id": "litellm_call_xyz",
      "error": "customer \"cus_unknown\" not found (set user=<external_customer_id> on the LiteLLM call)"
    }
  ]
}
```

`skipped` covers non-token-bearing calls (image generation, moderation) and zero-token failed completions. `errors[]` lists per-row reasons. 5xx is reserved for full-handler failure (DB down, etc.) — operator-side misconfig never returns 5xx.

## Caveats

- **Cost figures**: LiteLLM's `response_cost` is stored on each event's `metadata.velox.litellm_response_cost_usd` but does NOT drive Velox billing. The billable amount is computed by Velox's pricing rules. Operators can reconcile by querying both — they won't always match (Velox uses your configured rates, LiteLLM uses its built-in cost table).
- **Meters must exist**: missing meter → per-row 422. Same path as missing `user`.
- **Single tenant per API key**: each Velox API key pins to one tenant. Multi-tenant LiteLLM proxies route via separate API keys per tenant (not a metadata field on the call).
