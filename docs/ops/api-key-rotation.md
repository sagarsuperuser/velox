# Velox — API Key Rotation

## Why rotate

Rotate an API key when:

1. **Routine hygiene.** Tenants should rotate production secrets on a cadence (90 days is a common choice). Unused long-lived secrets are the single largest category of compromised credentials in SaaS incident postmortems.
2. **Role change.** An engineer who had access is leaving, changing teams, or losing admin permissions. Rotate before access is needed by the next owner.
3. **Suspected exposure.** A key appeared in a log line, a commit, a screenshot, a chat transcript, a former employee's laptop, or an external pastebin. Treat it as compromised and rotate immediately.
4. **Platform migration.** Moving from one deployment or region to another and you want a clean credential boundary per environment.

There is no such thing as over-rotation. Every rotation shortens the window during which a leaked key is useful.

## Endpoint

```
POST /v1/api-keys/{id}/rotate
Content-Type: application/json

{
  "expires_in_seconds": 0
}
```

Request body is optional — an empty body means immediate rotation.

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `expires_in_seconds` | integer | `0` | Seconds the old key stays valid after rotation. `0` revokes the old key immediately. Capped at `604800` (7 days). |

Response:

```json
{
  "old_key":  { ... APIKey with revoked_at or expires_at set ... },
  "new_key":  { ... APIKey, same name/type/livemode/expires_at as old ... },
  "raw_key":  "vlx_secret_live_d1e2f3a4b5c6..."
}
```

The `raw_key` is shown **exactly once**. Velox stores only a salted SHA-256 hash — the server literally cannot return the raw key again. If you lose it, rotate again.

## Choosing a grace period

### `expires_in_seconds: 0` (default) — immediate rotation

Use when:

- The key is known-compromised.
- You're operating a single-instance deployment that can swap credentials in one restart.
- You're rotating an admin / platform key that is only used interactively.

Trade-off: any request in flight under the old key continues to complete, but the next request using the old key returns 401. Automated systems that haven't picked up the new secret will fail until they do.

### `expires_in_seconds: 3600` (1 hour) — short grace window

Use when:

- You're rotating a deployed service key and want a deploy window.
- Your CI pipeline can ship the new secret within the hour.

### `expires_in_seconds: 86400` (24 hours) — standard zero-downtime rotation

Use when:

- You run multiple environments or regions and roll changes sequentially.
- Scheduled maintenance windows are on a daily cadence.

### `expires_in_seconds: 604800` (7 days) — maximum grace

Use when:

- You need to coordinate with customers or third parties who hold the key.
- You have a weekly deployment rhythm and want the rotation to ride the train.

Velox caps grace at 7 days. Anything longer is indistinguishable from "never rotated" from an incident-response perspective.

## Operational guardrails

### Self-rotation is blocked

The endpoint refuses `POST /v1/api-keys/{id}/rotate` where `{id}` is the key that authenticated the request. Reason: if `expires_in_seconds: 0`, the caller's next request fails; the UX is that you "lost your own key" mid-operation.

To rotate the only key you have, authenticate with a different key first. For a fresh tenant with no other keys, the dashboard (not the API) is the escape hatch — it authenticates via session, not a key.

### Revoked keys cannot be rotated

A revoked key is already terminal. Rotating it would create a replacement with no usage history — not a rotation, a re-issue. Callers that want a replacement for a revoked key should `POST /v1/api-keys` and issue a fresh one.

### Mode is preserved

A test-mode key always rotates to a test-mode key, and a live-mode key always rotates to a live-mode key, regardless of the calling ctx's mode. Cross-mode rotation would silently replace production credentials with sandbox ones (or the reverse) — the class of mistake that causes a Monday-morning outage.

### The new key inherits `expires_at`

If the old key had an absolute expiry set, the new key carries the same expiry forward. This keeps a rotation from accidentally extending the lifetime of a key scheduled for retirement. If you want a longer-lived replacement, issue a new key explicitly instead of rotating.

## Rotation playbook for incidents

1. **Identify the compromised key.** Find the `key_prefix` in logs / leaked artifact. Match to a key via `GET /v1/api-keys` — the prefix is stored in the clear for exactly this lookup.
2. **Rotate with `expires_in_seconds: 0`.** No grace — a compromised key should stop working immediately.
3. **Save the new `raw_key`** to your secret store before closing the response window.
4. **Deploy the new secret.** The old key is already dead; every unupdated caller is now returning 401 and will need the new value.
5. **Audit recent activity under the old key.** The key's `last_used_at` and the `audit_log` table are your breadcrumbs.
6. **If the leak source is unknown**, rotate every key for the tenant. Better to force a fleet-wide deploy than to leave a second compromised key live.

## Rotation playbook for scheduled rotation

1. **Pick a grace window** matching your deploy cadence (24h is typical).
2. **Rotate** with `expires_in_seconds: 86400`.
3. **Store the new `raw_key`** and roll it through your secret store.
4. **Deploy.** Traffic migrates from old to new inside the grace window.
5. **Verify** via `GET /v1/api-keys/{id}` that the old key has `expires_at` set and that the new key is receiving traffic (`last_used_at` advancing).
6. **Let the grace window close.** The old key becomes invalid automatically — no cleanup required.

## What rotation does NOT do

- **It does not invalidate API key hashes stored in request logs.** If you log request headers somewhere, those still contain the old key hash — treat your observability pipeline's retention policy as part of your secret lifetime.
- **It does not re-issue webhooks or idempotency keys.** These are per-key artifacts that remain bound to the key that created them. Rotation does not orphan past webhook deliveries; they stay attributed to the retired key.
- **It does not notify customers.** The tenant's admins see a `key_rotated` audit event; there is no email / webhook to downstream consumers of the key.
