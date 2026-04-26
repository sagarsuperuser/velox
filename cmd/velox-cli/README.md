# velox-cli

Operator CLI for a running Velox deployment. Talks to the same `/v1/*`
HTTP surface external integrations use — no direct DB coupling, no
runtime config files. Authenticates with a platform API key from the
`VELOX_API_KEY` env var or the `--api-key` flag.

## Install

From source:

```sh
make cli                  # builds ./bin/velox-cli
```

Or directly via `go build`:

```sh
go build -o bin/velox-cli ./cmd/velox-cli
```

The CLI is a single static binary; copy `./bin/velox-cli` anywhere on
your `$PATH`.

## Configure

Two env vars cover the common case:

| Env             | Default                  | Notes                                           |
| --------------- | ------------------------ | ----------------------------------------------- |
| `VELOX_API_KEY` | (required)               | Platform API key from the Velox dashboard.      |
| `VELOX_API_URL` | `http://localhost:8080`  | Base URL — point at staging / prod as needed.   |

Equivalent flags `--api-key` and `--api-url` override the env. The CLI
never writes the key to disk.

## First command

```sh
export VELOX_API_KEY=vlx_secret_live_...
export VELOX_API_URL=https://api.velox.example.com

velox-cli sub list --status active --limit 10
```

Output:

```
ID         CUSTOMER     PLAN              STATUS    CURRENT_PERIOD_END
sub_001    cus_alpha    plan_pro          active    2026-05-01T00:00:00Z
sub_002    cus_beta     plan_starter      trialing  2026-05-15T00:00:00Z
```

Pipe JSON into another tool:

```sh
velox-cli sub list --output json | jq '.data[].id'
```

## Subcommands

### `sub list`

List subscriptions, filterable by customer, plan, or status.

```
Flags:
  --customer string  Filter by customer_id
  --plan     string  Filter by plan_id
  --status   string  Filter by status (trialing, active, paused, canceled)
  --limit    int     Maximum number to return (default 20)
  --output   string  text | json (default text)
```

### `invoice send`

Send a finalized invoice as a PDF email attachment. Wraps
`POST /v1/invoices/{id}/send`.

```
Flags:
  --invoice  string  Invoice ID (required)
  --email    string  Recipient email (required unless --dry-run)
  --dry-run  bool    Print the request shape without calling the API
  --output   string  text | json (default text)
```

Example:

```sh
velox-cli invoice send --invoice in_001 --email billing@customer.com
```

Dry-run preview (no network call):

```sh
velox-cli invoice send --invoice in_001 --email billing@customer.com --dry-run
```

## Exit codes

- `0` — success.
- `1` — API error, network error, missing flag, or invalid `--output`.

## Roadmap

This is the Week 7 cut. Future operator surfaces (bulk operations,
one-off invoice composer, `import-from-stripe` from the Week 11 RFC)
will land as additional subcommands under the same root.
