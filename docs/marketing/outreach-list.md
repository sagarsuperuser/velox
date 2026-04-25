# Velox — Design-Partner Outreach List (50)

> Compiled 2026-04-25 for Track B / Week 1 of the [90-day plan](../90-day-plan.md).
> Target: 3 design partners with signed LOI by Day 60 (Week 8). 12 months free
> hosted access in exchange for weekly check-in and co-branded case study.

## How to use this list

Each row below is a **lead**, not a confirmed contact. Names listed are public-facing
founders/CEOs/CTOs whose roles were widely indexed as of late 2025/early 2026; verify
on LinkedIn or the company's `/about` page before any outbound email — engineering
leadership rotates faster than founder seats, and a stale title burns the first
impression.

Outreach priority is driven by wedge fit, not company size:

1. **Pricing-matrix complexity** — published rate cards with model × operation ×
   cache × tier dimensions ≥ 12 cells. These teams already feel the Stripe Meter
   API pain.
2. **Regulatory exposure** — EU GDPR-strict, India RBI data-localization, healthcare,
   gov-procurement adjacency. Self-host pillar lands hardest here.
3. **Stripe Billing fee surface** — companies likely paying ≥ $5K/month in Stripe
   Billing fees (rough proxy: $1M+ ARR, usage-billed product, cards via Stripe).
4. **Engineering-led decision** — VP Eng or staff/principal engineer can sign-off
   without procurement.

Tiers below are coarse: **A** = strong wedge fit + prior signal of pricing-model
pain (top of pipeline), **B** = good fit, less public pain signal, **C** = adjacent
fit (worth one touch).

The spreadsheet-style table below is the working document. Pre-send checklist:

- [ ] Verify role on LinkedIn within last 60 days
- [ ] Read their public pricing page; confirm wedge applies
- [ ] First-line opener references a specific dimension of their rate card or a
      regulatory constraint they've publicly mentioned

---

## AI inference & model APIs (22)

| #  | Tier | Company        | URL                  | Contact (verify)            | Role                  | Why fit                                                                                          |
|----|------|----------------|----------------------|-----------------------------|------------------------|--------------------------------------------------------------------------------------------------|
| 1  | A    | Replicate      | replicate.com        | Ben Firshman               | CEO / co-founder       | Per-second compute billing across hundreds of models — multi-dim rate card on day one.           |
| 2  | A    | Together AI    | together.ai          | Vipul Ved Prakash          | CEO / co-founder       | Hosted inference for OSS LLMs; per-token pricing across model × operation, scaling fast.         |
| 3  | A    | Fireworks AI   | fireworks.ai         | Lin Qiao                   | CEO / co-founder       | Fast LLM inference with custom-model surcharges and serverless tiers; rate matrix is large.      |
| 4  | A    | Modal          | modal.com            | Erik Bernhardsson          | CEO / founder          | Per-second GPU billing across A10/A100/H100 tiers + CPU + storage. Erik writes about infra publicly. |
| 5  | A    | Baseten        | baseten.co           | Tuhin Srivastava           | CEO / co-founder       | Custom model serving; per-replica + per-second + per-request dimensions.                          |
| 6  | A    | Anyscale       | anyscale.com         | Robert Nishihara           | CEO / co-founder       | Ray-based platform; usage-billed compute + memory + GPU dimensions.                              |
| 7  | B    | RunPod         | runpod.io            | Pardeep Singh              | CEO / co-founder       | GPU-by-the-second cloud; community vs. secure-cloud tiers compound the rate matrix.              |
| 8  | A    | Lepton AI      | lepton.ai            | Yangqing Jia               | CEO / founder          | Founder built Caffe + ONNX; AI infra company likely to engage with technical billing argument.   |
| 9  | B    | Groq           | groq.com             | Jonathan Ross              | CEO / founder          | LPU inference; speculative on billing model but well-known in inference space.                   |
| 10 | B    | SambaNova      | sambanova.ai         | Rodrigo Liang              | CEO / co-founder       | Enterprise inference; data-residency conversations come up naturally.                            |
| 11 | A    | Cerebras Cloud | cerebras.net         | Andrew Feldman             | CEO / co-founder       | Wafer-scale inference cloud; usage-billed by tokens × model × tier.                              |
| 12 | B    | Cohere         | cohere.com           | Aidan Gomez                | CEO / co-founder       | Enterprise LLM API with command/embed/rerank — multi-dimensional from day one.                   |
| 13 | C    | Mistral AI     | mistral.ai           | Arthur Mensch              | CEO / co-founder       | EU-based LLM API; GDPR-strict by default, multi-model rate card.                                 |
| 14 | A    | ElevenLabs     | elevenlabs.io        | Mati Staniszewski          | CEO / co-founder       | Voice gen billed per character × voice tier × model — pricing matrix already complex.            |
| 15 | A    | Deepgram       | deepgram.com         | Scott Stephenson           | CEO / co-founder       | Speech-to-text API across model tiers, languages, batch vs. streaming — multi-dim everywhere.    |
| 16 | A    | AssemblyAI     | assemblyai.com       | Dylan Fox                  | CEO / founder          | Speech AI APIs with per-second + per-feature (diarization, sentiment) billing.                   |
| 17 | B    | Hume AI        | hume.ai              | Alan Cowen                 | CEO / co-founder       | Empathic voice + emotion APIs; novel-feature pricing tends toward dimensional.                   |
| 18 | C    | RunwayML       | runwayml.com         | Cris Valenzuela            | CEO / co-founder       | Generative video; per-second × resolution × model rate cells.                                    |
| 19 | C    | Pika           | pika.art             | Demi Guo                   | CEO / co-founder       | Video generation, consumer-leaning today — verify if a self-serve API exists yet.                |
| 20 | C    | Suno           | suno.com             | Mikey Shulman              | CEO / co-founder       | Music generation; subscription today, API may be on roadmap.                                     |
| 21 | C    | Perplexity     | perplexity.ai        | Aravind Srinivas           | CEO / co-founder       | Sonar API + per-token billing; large enough that a wedge-fit conversation is worth one touch.    |
| 22 | C    | Friendli AI    | friendli.ai          | Byung-Gon Chun             | CEO                    | Korean inference platform with token-batching billing — verify English-language outreach norms.  |

## Vector DB & RAG infra (9)

| #  | Tier | Company       | URL              | Contact (verify)         | Role                  | Why fit                                                                                            |
|----|------|---------------|------------------|--------------------------|------------------------|----------------------------------------------------------------------------------------------------|
| 23 | A    | Pinecone      | pinecone.io      | Edo Liberty             | CEO / founder          | Serverless tier billed by storage × reads × writes — three-dim minimum.                            |
| 24 | A    | Weaviate      | weaviate.io      | Bob van Luijt           | CEO / co-founder       | OSS-first vector DB with hosted SaaS; tier × dimension × storage rate cells.                       |
| 25 | A    | Qdrant        | qdrant.tech      | Andre Zayarni           | CEO / co-founder       | OSS vector DB with cloud; rate card is RAM × storage × throughput.                                 |
| 26 | B    | Chroma        | trychroma.com    | Jeff Huber              | CEO / co-founder       | Embedding DB; cloud product is usage-billed and growing.                                           |
| 27 | A    | LanceDB       | lancedb.com      | Chang She               | CEO / co-founder       | Multimodal vector DB; usage billing across compute, storage, retrieval.                            |
| 28 | A    | Turbopuffer   | turbopuffer.com  | Simon Hørup Eskildsen   | Founder / CEO          | Serverless vector search billed per-query × storage; Simon is a careful systems writer publicly.   |
| 29 | B    | Zilliz / Milvus | zilliz.com     | Charles Xie             | CEO / founder          | Hosted Milvus; vector storage + read/write tiers.                                                  |
| 30 | C    | Marqo         | marqo.ai         | Tom Hamer               | CEO / co-founder       | Multimodal embeddings as a service; smaller, may welcome design-partner deal.                      |
| 31 | C    | Vespa         | vespa.ai         | Jon Bratseth            | CEO                    | Yahoo spin-out; enterprise search/vector. Larger eng org but data-sovereignty pillar lands.        |

## Dev infra & API platforms (19)

| #  | Tier | Company        | URL              | Contact (verify)        | Role                  | Why fit                                                                                          |
|----|------|----------------|------------------|-------------------------|------------------------|--------------------------------------------------------------------------------------------------|
| 32 | A    | Resend         | resend.com       | Zeno Rocha             | CEO / co-founder       | Email API with per-email + tier-based pricing; small team likely to feel Stripe Billing limits.  |
| 33 | A    | Knock          | knock.app        | Chris Bell             | CEO / co-founder       | Notifications API; per-message × channel rate cells.                                             |
| 34 | A    | Svix           | svix.com         | Tom Hacohen            | CEO / co-founder       | Webhooks-as-a-service, OSS-first. Tom blogs technically — likely receptive.                      |
| 35 | B    | Hookdeck       | hookdeck.com     | Alex Bouchard          | CEO / co-founder       | Webhook reliability platform; usage-billed by event volume.                                      |
| 36 | A    | Inngest        | inngest.com      | Tony Holdstock-Brown   | CEO / co-founder       | Durable workflows; per-step × per-second compute billing — multi-dim.                            |
| 37 | A    | Trigger.dev    | trigger.dev      | Eric Allam             | CEO / co-founder       | Background job platform; per-execution + duration billing.                                       |
| 38 | A    | Convex         | convex.dev       | James Cowling          | CEO / co-founder       | Backend platform billed per-function × bandwidth × storage; usage-heavy.                         |
| 39 | B    | Liveblocks     | liveblocks.io    | Steven Fabre           | CEO / co-founder       | Realtime collaboration infra; per-MAU × per-room dimensions.                                     |
| 40 | A    | PostHog        | posthog.com      | James Hawkins          | CEO / co-founder       | Product analytics; OSS + cloud, usage-billed by events × features. Known for OSS empathy.        |
| 41 | A    | Tinybird       | tinybird.co      | Jorge Sancha           | CEO / co-founder       | Real-time analytics API; per-query × per-row × storage rate matrix.                              |
| 42 | A    | Axiom          | axiom.co         | Neil Jagdish Patel     | CEO / co-founder       | Observability platform billed by ingest × storage × query — three-dim minimum.                   |
| 43 | B    | Highlight      | highlight.io     | Vadim Korolik          | CEO / co-founder       | Session replay + observability; per-session × retention dimensions.                              |
| 44 | A    | Supabase       | supabase.com     | Paul Copplestone       | CEO / co-founder       | OSS Postgres-as-a-service; usage-billed by compute × storage × egress + multi-region.            |
| 45 | A    | Neon           | neon.tech        | Nikita Shamgunov       | CEO / co-founder       | Serverless Postgres billed per CU-hour × storage × egress; usage-heavy and growing fast.         |
| 46 | A    | Turso          | turso.tech       | Glauber Costa          | CEO / co-founder       | Edge SQLite billed per-read × per-write × storage × replica region.                              |
| 47 | B    | PlanetScale    | planetscale.com  | Sam Lambert            | CEO                    | MySQL platform with usage-based tiers; large eng team but pricing complexity is real.            |
| 48 | C    | Hasura         | hasura.io        | Tanmai Gopal           | CEO / co-founder       | GraphQL engine; v3 cloud is usage-billed across requests, rows, regions.                         |
| 49 | B    | E2B            | e2b.dev          | Vasek Mlejnsky         | CEO / co-founder       | Code-execution sandboxes for AI agents; per-second × resource billing — emerging multi-dim.      |
| 50 | B    | Browserbase    | browserbase.com  | Paul Klein IV          | CEO / co-founder       | Headless-browser API for AI agents; per-session × per-minute × proxy-tier rate cells.            |

---

## Outreach sequencing (suggested)

- **Days 1–7 (Week 1):** verify roles, draft per-segment cold-email templates, set up `partners@velox.dev` and a single-page tracker.
- **Days 8–14 (Week 2):** send to **all 14 Tier-A AI-inference rows** (#1–6, 8, 11, 14–16) — strongest wedge fit.
- **Days 15–21 (Week 3):** Tier-A vector DB (#23–25, 27–28) + Tier-A dev infra (#32–34, 36–38, 40–42, 44–46).
- **Days 22–28 (Week 4):** Tier-B follow-ups; second touch for any Tier-A non-responses.
- **Days 29–60:** book demo calls; convert to LOI on the 12-months-free-for-case-study deal.

## Templates needed (separate doc, Week 2)

- AI inference cold email (lead with the multi-dim rate-card observation; reference their pricing page specifically)
- Vector DB cold email (lead with self-host + data sovereignty)
- Dev infra cold email (lead with the Stripe Meter API friction post)
- Founder-to-founder vs. CEO-to-VP-Eng tone variants

## What's deliberately not on this list

- **Anthropic, OpenAI, Google AI, Microsoft AI, Meta AI** — they build their own billing; not customers.
- **Pre-seed companies** — usually still on Stripe Billing's free tier, not feeling the friction.
- **Marketplace / Connect-pattern companies** — out of wedge per [`docs/positioning.md`](../positioning.md).
- **Consumer-only AI apps** (no API, no usage billing) — wrong shape.
- **Hyperscaler internal teams** (AWS, GCP, Azure) — internal billing is a different problem domain.
