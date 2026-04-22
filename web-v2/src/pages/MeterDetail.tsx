import { useParams, Link, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, formatCents, formatDateTime } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { statusBadgeVariant } from '@/lib/status'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'

import { Loader2 } from 'lucide-react'
import { CopyButton } from '@/components/CopyButton'
import { DetailBreadcrumb } from '@/components/DetailBreadcrumb'

const statusVariant = statusBadgeVariant

export default function MeterDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const { data: meter, isLoading: meterLoading, error: meterError, refetch } = useQuery({
    queryKey: ['meter', id],
    queryFn: () => api.getMeter(id!),
    enabled: !!id,
  })

  const { data: ratingRule } = useQuery({
    queryKey: ['meter-rating-rule', meter?.rating_rule_version_id],
    queryFn: async () => {
      if (!meter?.rating_rule_version_id) return null
      const linkedRule = await api.getRatingRule(meter.rating_rule_version_id)
      try {
        const allRules = await api.listRatingRules()
        const sameKey = allRules.data.filter(r => r.rule_key === linkedRule.rule_key)
        if (sameKey.length > 0) {
          return sameKey.reduce((a, b) => b.version > a.version ? b : a)
        }
        return linkedRule
      } catch {
        return linkedRule
      }
    },
    enabled: !!meter?.rating_rule_version_id,
  })

  const { data: plans } = useQuery({
    queryKey: ['meter-plans', id],
    queryFn: async () => {
      const res = await api.listPlans()
      return res.data.filter(p => p.meter_ids?.includes(id!))
    },
    enabled: !!id,
  })

  const loading = meterLoading
  const error = meterError instanceof Error ? meterError.message : meterError ? String(meterError) : null

  const graduatedTierLabel = (tier: { up_to: number }, index: number, tiers: { up_to: number }[]) => {
    const prev = index > 0 ? tiers[index - 1].up_to : 0
    if (tier.up_to === 0 || tier.up_to === -1) {
      return `Beyond ${prev.toLocaleString()} units`
    }
    if (index === 0) {
      return `First ${tier.up_to.toLocaleString()} units`
    }
    return `Next ${(tier.up_to - prev).toLocaleString()} units`
  }

  if (loading) {
    return (
      <Layout>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Link to="/pricing" className="hover:text-foreground transition-colors">Pricing</Link>
          <span>/</span>
          <span>Loading...</span>
        </div>
        <div className="flex justify-center py-16">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      </Layout>
    )
  }

  if (error) {
    return (
      <Layout>
        <div className="py-16 text-center">
          <p className="text-sm text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" onClick={() => refetch()}>Retry</Button>
        </div>
      </Layout>
    )
  }

  if (!meter) {
    return (
      <Layout>
        <p className="text-sm text-muted-foreground py-16 text-center">Meter not found</p>
      </Layout>
    )
  }

  return (
    <Layout>
      <DetailBreadcrumb to="/pricing" parentLabel="Pricing" currentLabel={meter.name} />

      {/* Header */}
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-semibold text-foreground">{meter.name}</h1>
          <div className="flex items-center gap-2 mt-1">
            <span className="text-xs text-muted-foreground font-mono bg-muted px-2 py-0.5 rounded">{meter.id}</span>
            <CopyButton text={meter.id} />
          </div>
        </div>
        <Badge variant="secondary">{meter.aggregation}</Badge>
      </div>

      {/* Properties */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Properties</CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          <div className="divide-y divide-border">
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Key</span>
              <span className="text-sm text-foreground font-mono">{meter.key}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Unit</span>
              <span className="text-sm text-foreground">{meter.unit}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Aggregation</span>
              <Badge variant="secondary">{meter.aggregation}</Badge>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">Created</span>
              <span className="text-sm text-foreground">{formatDateTime(meter.created_at)}</span>
            </div>
            <div className="flex items-center justify-between px-6 py-3">
              <span className="text-sm text-muted-foreground">ID</span>
              <div className="flex items-center gap-2">
                <span className="text-sm text-foreground font-mono truncate max-w-xs" title={meter.id}>{meter.id}</span>
                <CopyButton text={meter.id} />
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Pricing Rule */}
      <Card className="mt-6">
        <CardHeader>
          <div className="flex items-center justify-between">
            <div>
              <CardTitle className="text-sm">Pricing Rule</CardTitle>
              {ratingRule && (
                <p className="text-sm text-muted-foreground mt-0.5">{ratingRule.name}</p>
              )}
            </div>
            {ratingRule && (
              <Badge variant="secondary">v{ratingRule.version}</Badge>
            )}
          </div>
        </CardHeader>
        <CardContent>
          {ratingRule ? (
            <div>
              <div className="flex items-center gap-2 mb-4">
                <Badge variant="info">{ratingRule.mode}</Badge>
                {ratingRule.currency && (
                  <span className="text-xs text-muted-foreground font-medium uppercase">{ratingRule.currency}</span>
                )}
              </div>

              {ratingRule.mode === 'flat' && (
                <div>
                  <span className="text-3xl font-semibold text-foreground">{formatCents(ratingRule.flat_amount_cents)}</span>
                  <span className="text-sm text-muted-foreground ml-2">per unit</span>
                </div>
              )}

              {ratingRule.mode === 'graduated' && ratingRule.graduated_tiers && (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Tier</TableHead>
                      <TableHead className="text-right">Price / unit</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {ratingRule.graduated_tiers.map((tier, i) => (
                      <TableRow key={i}>
                        <TableCell>{graduatedTierLabel(tier, i, ratingRule.graduated_tiers!)}</TableCell>
                        <TableCell className="text-right">{formatCents(tier.unit_amount_cents)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}

              {ratingRule.mode === 'package' && (
                <div>
                  <span className="text-3xl font-semibold text-foreground">{ratingRule.package_size.toLocaleString()}</span>
                  <span className="text-sm text-muted-foreground ml-2">units per package at</span>
                  <span className="text-lg font-semibold text-foreground ml-1">{formatCents(ratingRule.package_amount_cents)}</span>
                </div>
              )}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground text-center py-4">No pricing rule linked</p>
          )}
        </CardContent>
      </Card>

      {/* Plans */}
      <Card className="mt-6">
        <CardHeader>
          <CardTitle className="text-sm">
            Used by {plans?.length ?? 0} plan{(plans?.length ?? 0) !== 1 ? 's' : ''}
          </CardTitle>
        </CardHeader>
        <CardContent className="p-0">
          {plans && plans.length > 0 ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Code</TableHead>
                  <TableHead>Interval</TableHead>
                  <TableHead className="text-right">Base Price</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {plans.map(plan => (
                  <TableRow
                    key={plan.id}
                    className="cursor-pointer hover:bg-muted/50 transition-colors"
                    onClick={(e) => {
                      const target = e.target as HTMLElement
                      if (target.closest('button, a, input, select')) return
                      navigate(`/plans/${plan.id}`)
                    }}
                  >
                    <TableCell>
                      <Link to={`/plans/${plan.id}`} className="text-sm font-medium text-foreground hover:text-primary transition-colors">
                        {plan.name}
                      </Link>
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground font-mono">{plan.code}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">{plan.billing_interval}</TableCell>
                    <TableCell className="text-sm font-medium text-foreground text-right">{formatCents(plan.base_amount_cents)}</TableCell>
                    <TableCell><Badge variant={statusVariant(plan.status)}>{plan.status}</Badge></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-sm text-muted-foreground text-center py-8">No plans are currently using this meter</p>
          )}
        </CardContent>
      </Card>
    </Layout>
  )
}
