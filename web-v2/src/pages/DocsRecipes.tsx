import { PublicLayout } from '@/components/PublicLayout'
import {
  DocsShell,
  DocsH1,
  DocsH2,
  DocsLead,
  Prose,
  Code,
  InlineCode,
  Callout,
} from '@/components/DocsShell'

// GitHub permalinks for the YAML files. Linking to a tagged ref keeps the
// docs page stable when the recipes change shape post-1.0; for now we link
// to the canonical main path and let operators fork from a known branch.
const REPO = 'https://github.com/sagarsuperuser/velox/blob/main/internal/recipe/recipes'

const recipes: {
  key: string
  name: string
  oneLine: string
  creates: string
  bestFor: string
}[] = [
  {
    key: 'anthropic_style',
    name: 'Anthropic-style AI inference',
    oneLine:
      'Per-token billing across model × operation × cached for the Claude 3 / 3.5 family.',
    creates:
      '1 meter (tokens), 9 rating rules, 10 pricing rules, 1 plan, 1 dunning policy (4 retries), 5 webhook events.',
    bestFor:
      'LLM inference platforms reselling Claude API access with a cache discount on input tokens.',
  },
  {
    key: 'openai_style',
    name: 'OpenAI-style AI inference',
    oneLine:
      'Per-token billing for the GPT family plus three embedding models, with cached-input rates.',
    creates:
      '1 meter (tokens), 14 rating rules, 14 pricing rules, 1 plan, 1 dunning policy (4 retries), 5 webhook events.',
    bestFor:
      'Inference platforms reselling OpenAI API access with separate embeddings pricing.',
  },
  {
    key: 'replicate_style',
    name: 'Replicate-style GPU-time billing',
    oneLine:
      'Per-second compute billing across {a100, a40, t4, cpu} hardware classes.',
    creates:
      '1 meter (gpu_seconds), 4 rating rules, 4 pricing rules, 1 plan, 1 dunning policy (4 retries), 5 webhook events.',
    bestFor:
      'Inference platforms that bill by hardware time rather than tokens.',
  },
  {
    key: 'b2b_saas_pro',
    name: 'B2B SaaS — seats + storage',
    oneLine:
      'Classic SaaS profile: per-seat with included tier and per-seat overage, plus a metered storage add-on.',
    creates:
      '2 meters (seats, storage_gb), 2 rating rules (graduated seat tiers + flat storage), 2 pricing rules, 1 plan ($99 base / 10 included seats), 1 dunning policy (3 retries), 5 webhook events.',
    bestFor:
      'B2B SaaS picking Velox for compliance / multi-tenant safety rather than AI-native features.',
  },
  {
    key: 'marketplace_gmv',
    name: 'Marketplace — GMV take rate',
    oneLine:
      'Percentage-of-volume marketplace billing with a per-transaction fee.',
    creates:
      '2 meters (gmv_cents, transactions), 2 rating rules (2.5% take rate via package billing + 30¢ per transaction), 2 pricing rules, 1 plan, 1 dunning policy (3 retries), 5 webhook events.',
    bestFor:
      'Etsy / eBay / Shopify-style marketplaces running their own seller billing without Stripe Connect.',
  },
]

export default function DocsRecipesPage() {
  return (
    <PublicLayout>
      <DocsShell>
        <Prose>
          <DocsH1>Pricing recipes</DocsH1>
          <DocsLead>
            A recipe is a YAML-defined bundle of meters, rating rules, pricing rules, a plan, a
            dunning policy, and a webhook subscription that gets instantiated atomically into a
            tenant. Skip the 30-minute manual setup; start from a proven pricing structure for your
            domain and tune from there.
          </DocsLead>

          <DocsH2>Five built-in recipes</DocsH2>
          <p>
            Velox ships five recipes in the binary today, loaded once at boot from{' '}
            <InlineCode>internal/recipe/recipes/*.yaml</InlineCode>. Each card below summarises what
            instantiating the recipe creates; see the YAML for full inspection.
          </p>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3 my-4">
            {recipes.map((r) => (
              <a
                key={r.key}
                href={`${REPO}/${r.key}.yaml`}
                target="_blank"
                rel="noreferrer"
                className="group block border border-border rounded-xl p-4 hover:border-primary/40 hover:bg-muted/40 transition-colors"
              >
                <div className="flex items-baseline justify-between gap-2 mb-1">
                  <h3 className="font-medium text-foreground">{r.name}</h3>
                  <code className="font-mono text-[11px] text-muted-foreground shrink-0">
                    {r.key}
                  </code>
                </div>
                <p className="text-sm text-muted-foreground leading-relaxed mb-3">{r.oneLine}</p>
                <dl className="text-[13px] space-y-1.5">
                  <div>
                    <dt className="inline font-medium text-foreground">Creates: </dt>
                    <dd className="inline text-muted-foreground">{r.creates}</dd>
                  </div>
                  <div>
                    <dt className="inline font-medium text-foreground">Best for: </dt>
                    <dd className="inline text-muted-foreground">{r.bestFor}</dd>
                  </div>
                </dl>
                <div className="text-[11px] text-muted-foreground mt-3 group-hover:text-primary transition-colors">
                  View YAML on GitHub →
                </div>
              </a>
            ))}
          </div>

          <DocsH2>Instantiate via API</DocsH2>
          <p>
            Posting to <InlineCode>/v1/recipes/{'{key}'}/instantiate</InlineCode> builds the entire
            object graph under one transaction — meters, rating rules, pricing rules, plan, dunning
            policy, and webhook subscription — then returns a{' '}
            <InlineCode>recipe_instance</InlineCode> with the IDs of every created object so you can
            deep-link straight to it. Body fields:
          </p>
          <ul className="list-disc pl-6 space-y-1 text-sm">
            <li>
              <InlineCode>overrides</InlineCode> — object-shaped, recipe-specific. Each recipe
              declares an <InlineCode>overridable</InlineCode> schema (e.g.{' '}
              <InlineCode>currency</InlineCode>, <InlineCode>plan_name</InlineCode>,{' '}
              <InlineCode>plan_code</InlineCode>) with defaults.
            </li>
            <li>
              <InlineCode>force</InlineCode> — reserved for v2; pass <InlineCode>false</InlineCode>{' '}
              or omit. Sending <InlineCode>true</InlineCode> currently returns{' '}
              <InlineCode>invalid_state</InlineCode>.
            </li>
          </ul>

          <p className="mt-4">
            <strong>Anthropic-style.</strong> Override currency and the plan slug; everything else
            comes from the recipe defaults.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/recipes/anthropic_style/instantiate \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "overrides": {
      "currency": "USD",
      "plan_name": "Claude API",
      "plan_code": "claude_api_pro"
    }
  }'`}</Code>

          <p>
            <strong>B2B SaaS.</strong> Same shape — the override keys differ per recipe (here we
            also tweak the included seat count).
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/recipes/b2b_saas_pro/instantiate \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "overrides": {
      "currency": "USD",
      "plan_name": "Pro",
      "plan_code": "saas_pro",
      "included_seats": 25
    }
  }'`}</Code>

          <p>
            On success the response is <InlineCode>201 Created</InlineCode> with a{' '}
            <InlineCode>RecipeInstance</InlineCode> body — <InlineCode>id</InlineCode>,{' '}
            <InlineCode>recipe_key</InlineCode>, <InlineCode>recipe_version</InlineCode>, the
            applied <InlineCode>overrides</InlineCode>, and a{' '}
            <InlineCode>created_objects</InlineCode> map keyed by entity type with the new row IDs.
          </p>

          <Callout tone="info">
            Recipes are idempotent on <InlineCode>(tenant_id, recipe_key)</InlineCode>. A second
            instantiate of the same recipe in the same tenant returns{' '}
            <InlineCode>already_exists</InlineCode> with the prior instance ID, not a duplicate
            graph. Send <InlineCode>Idempotency-Key</InlineCode> on the request itself for safe
            retries on transient errors. See{' '}
            <a href="/docs/idempotency" className="underline underline-offset-2 hover:text-foreground">
              Idempotency &amp; retries
            </a>
            .
          </Callout>

          <DocsH2>Preview before instantiate</DocsH2>
          <p>
            <InlineCode>POST /v1/recipes/{'{key}'}/preview</InlineCode> renders the recipe with your
            overrides and returns the would-be-created object graph without touching the database.
            Cheap enough to call on every override-form keystroke; useful for showing operators
            exactly what they're about to create before they commit.
          </p>
          <Code language="shell">{`curl -X POST $VELOX_API/recipes/anthropic_style/preview \\
  -H "Authorization: Bearer $VELOX_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{ "overrides": { "currency": "USD" } }'`}</Code>

          <p>The response shape:</p>
          <Code language="json">{`{
  "key": "anthropic_style",
  "version": "1.0.0",
  "objects": {
    "meters": [ ... ],
    "rating_rules": [ ... ],
    "pricing_rules": [ ... ],
    "plans": [ ... ],
    "dunning_policies": [ ... ],
    "webhook_endpoints": [ ... ]
  },
  "warnings": []
}`}</Code>
          <p className="text-sm text-muted-foreground">
            Optional pieces (dunning policy, webhook subscription) are emitted as 0-or-1-length
            arrays so the wire shape is uniform and clients iterate without null guards. Empty
            arrays are emitted (no <InlineCode>omitempty</InlineCode>) for the same reason.
          </p>

          <DocsH2>Atomicity guarantees</DocsH2>
          <p>
            Instantiate runs under one Postgres transaction. The graph is built in a strict order
            — rating rules → meters → pricing rules → plan → (optional) dunning policy → (optional)
            webhook endpoint → instance row — and any failure rolls the whole graph back. Partial
            state never reaches the tenant. RLS keeps every write scoped to the calling tenant; a
            recipe instantiate from tenant A cannot leak rows into tenant B even under contention.
          </p>

          <DocsH2>Listing &amp; uninstalling</DocsH2>
          <p>
            <InlineCode>GET /v1/recipes</InlineCode> returns the registry plus a per-tenant install
            flag so the dashboard can show which recipes a tenant has already instantiated.{' '}
            <InlineCode>GET /v1/recipes/instances</InlineCode> returns the installed instances with
            their <InlineCode>created_objects</InlineCode> maps.{' '}
            <InlineCode>DELETE /v1/recipes/instances/{'{id}'}</InlineCode> removes the instance row
            without cascading deletes — the meters / rating rules / plan / dunning policy / webhook
            it created stay in place. Operators tear those down individually if they want a clean
            slate.
          </p>

          <DocsH2>Customising recipes</DocsH2>
          <p>
            Recipes are plain YAML at <InlineCode>internal/recipe/recipes/</InlineCode> compiled into
            the Velox binary at build time. To add your own, fork the repo, drop a new YAML in that
            directory, rebuild, and the registry picks it up at boot. The full schema — recipe
            structure, overridable types, the rating-rule modes recipes can express, and the
            renderer's templating rules — lives in{' '}
            <InlineCode>docs/design-recipes.md</InlineCode>.
          </p>
          <Callout tone="info">
            Velox does not currently load recipes from disk at runtime; a recipe change requires a
            redeploy. Hot-reloading from a tenant-managed directory is on the roadmap and will land
            when the operator persona expands beyond the Velox core team.
          </Callout>
        </Prose>
      </DocsShell>
    </PublicLayout>
  )
}
