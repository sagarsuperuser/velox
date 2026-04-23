import { CheckCircle2 } from 'lucide-react'
import { PublicLayout, PublicPageHeader } from '@/components/PublicLayout'

const components = [
  { name: 'API', description: 'REST API at api.velox.dev' },
  { name: 'Dashboard', description: 'Operator console' },
  { name: 'Webhook delivery', description: 'Outbound signed events' },
  { name: 'Stripe integration', description: 'Payment intent orchestration' },
  { name: 'Background scheduler', description: 'Invoice generation and dunning' },
]

export default function StatusPage() {
  return (
    <PublicLayout>
      <PublicPageHeader
        eyebrow="Platform"
        title="System status"
        description="Live status of Velox services. During the design-partner phase, this page is updated manually — a fully automated hosted status page is on the roadmap."
      />
      <div className="max-w-4xl mx-auto px-6 py-12">
        <div className="border border-border rounded-xl p-6 bg-muted/30">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 flex items-center justify-center">
              <CheckCircle2 size={20} />
            </div>
            <div>
              <div className="font-medium text-foreground text-lg">All systems operational</div>
              <div className="text-sm text-muted-foreground">
                Last checked {new Date().toLocaleString()}
              </div>
            </div>
          </div>
        </div>

        <h2 className="text-lg font-semibold tracking-tight text-foreground mt-10 mb-4">Components</h2>
        <div className="border border-border rounded-xl overflow-hidden">
          {components.map((comp, i) => (
            <div
              key={comp.name}
              className={`flex items-center justify-between px-5 py-4 ${i > 0 ? 'border-t border-border' : ''}`}
            >
              <div>
                <div className="font-medium text-foreground">{comp.name}</div>
                <div className="text-sm text-muted-foreground">{comp.description}</div>
              </div>
              <div className="flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-emerald-500" />
                <span className="text-sm text-muted-foreground">Operational</span>
              </div>
            </div>
          ))}
        </div>

        <h2 className="text-lg font-semibold tracking-tight text-foreground mt-10 mb-4">
          Recent incidents
        </h2>
        <div className="border border-border rounded-xl p-10 text-center text-sm text-muted-foreground">
          No incidents reported in the last 30 days.
        </div>

        <h2 className="text-lg font-semibold tracking-tight text-foreground mt-10 mb-3">
          Subscribe to updates
        </h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          During design-partner onboarding, incident communication is direct — you'll hear from us by
          email and through your dedicated Slack channel. A public subscription feed (email, RSS,
          Slack integration) will ship with the hosted status page.
        </p>
      </div>
    </PublicLayout>
  )
}
