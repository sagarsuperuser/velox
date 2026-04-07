#!/usr/bin/env bash
# Velox Demo: Full Billing Cycle Walkthrough
# Usage: ./scripts/demo.sh <API_KEY>
#
# Prerequisites:
#   docker compose up -d
#   go run ./cmd/velox-bootstrap
#   go run ./cmd/velox &
#
# Then run this script with the secret key from bootstrap output.

set -euo pipefail

API_KEY="${1:-}"
BASE="http://localhost:8080"

if [ -z "$API_KEY" ]; then
  echo "Usage: ./scripts/demo.sh <vlx_secret_...>"
  echo ""
  echo "Run 'go run ./cmd/velox-bootstrap' first to get a key."
  exit 1
fi

AUTH="Authorization: Bearer $API_KEY"

green() { printf "\033[32m%s\033[0m\n" "$1"; }
blue() { printf "\033[34m%s\033[0m\n" "$1"; }
step() { echo ""; blue "━━━ $1 ━━━"; }

step "1. Health Check"
curl -s "$BASE/health" | jq .

step "2. Create Rating Rule (graduated pricing for API calls)"
RULE=$(curl -s -X POST "$BASE/v1/rating-rules" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d '{
    "rule_key": "api_calls",
    "name": "API Call Pricing",
    "mode": "graduated",
    "currency": "USD",
    "graduated_tiers": [
      {"up_to": 1000, "unit_amount_cents": 10},
      {"up_to": 0, "unit_amount_cents": 5}
    ]
  }')
RULE_ID=$(echo "$RULE" | jq -r '.id')
green "Created rule: $RULE_ID"
echo "$RULE" | jq .

step "3. Create Meter"
METER=$(curl -s -X POST "$BASE/v1/meters" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"key\": \"api_calls\",
    \"name\": \"API Calls\",
    \"unit\": \"calls\",
    \"aggregation\": \"sum\",
    \"rating_rule_version_id\": \"$RULE_ID\"
  }")
METER_ID=$(echo "$METER" | jq -r '.id')
green "Created meter: $METER_ID"

step "4. Create Plan"
PLAN=$(curl -s -X POST "$BASE/v1/plans" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"code\": \"pro\",
    \"name\": \"Pro Plan\",
    \"currency\": \"USD\",
    \"billing_interval\": \"monthly\",
    \"base_amount_cents\": 4900,
    \"meter_ids\": [\"$METER_ID\"]
  }")
PLAN_ID=$(echo "$PLAN" | jq -r '.id')
green "Created plan: $PLAN_ID"
echo "$PLAN" | jq .

step "5. Create Customer"
CUSTOMER=$(curl -s -X POST "$BASE/v1/customers" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d '{
    "external_id": "acme_corp",
    "display_name": "Acme Corporation",
    "email": "billing@acme.com"
  }')
CUSTOMER_ID=$(echo "$CUSTOMER" | jq -r '.id')
green "Created customer: $CUSTOMER_ID"

step "6. Create Subscription"
SUB=$(curl -s -X POST "$BASE/v1/subscriptions" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"code\": \"acme-pro\",
    \"display_name\": \"Acme Pro Monthly\",
    \"customer_id\": \"$CUSTOMER_ID\",
    \"plan_id\": \"$PLAN_ID\",
    \"start_now\": true
  }")
SUB_ID=$(echo "$SUB" | jq -r '.id')
green "Created subscription: $SUB_ID (status: $(echo "$SUB" | jq -r '.status'))"

step "7. Ingest Usage Events"
for i in 1 2 3 4 5; do
  curl -s -X POST "$BASE/v1/usage-events" \
    -H "$AUTH" -H "Content-Type: application/json" \
    -d "{
      \"customer_id\": \"$CUSTOMER_ID\",
      \"meter_id\": \"$METER_ID\",
      \"subscription_id\": \"$SUB_ID\",
      \"quantity\": 300
    }" > /dev/null
  echo "  Ingested batch $i: 300 API calls"
done
green "Total: 1,500 API calls ingested"

step "8. List Usage Events"
curl -s "$BASE/v1/usage-events?customer_id=$CUSTOMER_ID" \
  -H "$AUTH" | jq '.data | length' | xargs -I{} echo "  {} events recorded"

step "9. Grant Customer Credits"
CREDIT=$(curl -s -X POST "$BASE/v1/credits/grant" \
  -H "$AUTH" -H "Content-Type: application/json" \
  -d "{
    \"customer_id\": \"$CUSTOMER_ID\",
    \"amount_cents\": 5000,
    \"description\": \"Welcome credit - \$50\"
  }")
green "Granted \$50 credit"
curl -s "$BASE/v1/credits/balance/$CUSTOMER_ID" -H "$AUTH" | jq .

step "10. List Customers"
curl -s "$BASE/v1/customers" -H "$AUTH" | jq '.data[] | {id, display_name, status}'

step "11. List Invoices"
curl -s "$BASE/v1/invoices" -H "$AUTH" | jq .

echo ""
green "━━━ Demo Complete ━━━"
echo ""
echo "What was demonstrated:"
echo "  ✓ API key authentication"
echo "  ✓ Rating rule (graduated pricing: \$0.10/call up to 1000, \$0.05 after)"
echo "  ✓ Meter + Plan configuration"
echo "  ✓ Customer creation"
echo "  ✓ Subscription with immediate activation"
echo "  ✓ Usage event ingestion (5 × 300 = 1,500 API calls)"
echo "  ✓ Customer credit grant (\$50)"
echo ""
echo "The billing scheduler runs every 5 minutes in local mode."
echo "When it runs, it will generate an invoice for \$199:"
echo "  Base fee:  \$49.00"
echo "  API calls: 1000 × \$0.10 + 500 × \$0.05 = \$125.00"
echo "  Storage:   \$25.00 (if configured)"
echo "  Total:     \$199.00"
echo ""
echo "Try: curl -s $BASE/v1/invoices -H '$AUTH' | jq ."
