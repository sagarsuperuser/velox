import { useEffect, useState, useMemo } from 'react'
import { api, formatDateTime, type ApiKeyInfo } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'
import { Plus, Key, Shield, Eye, ChevronDown } from 'lucide-react'

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const d = new Date(dateStr).getTime()
  const diff = now - d
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'Just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return formatDateTime(dateStr)
}

export function ApiKeysPage() {
  const [keys, setKeys] = useState<ApiKeyInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [createdKey, setCreatedKey] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ApiKeyInfo | null>(null)
  const [isRevokingSelf, setIsRevokingSelf] = useState(false)
  const [showRevoked, setShowRevoked] = useState(false)
  const toast = useToast()

  const currentKeyPrefix = localStorage.getItem('velox_api_key')?.slice(0, 20) || ''

  const loadKeys = () => {
    setLoading(true)
    setError(null)
    api.listApiKeys()
      .then(res => { setKeys(res.data || []); setLoading(false) })
      .catch(err => { setError(err instanceof Error ? err.message : 'Failed to load API keys'); setKeys([]); setLoading(false) })
  }

  useEffect(() => { loadKeys() }, [])

  const handleRevoke = async () => {
    if (!revokeTarget) return
    try {
      await api.revokeApiKey(revokeTarget.id)
      toast.success('API key revoked')
      setRevokeTarget(null)
      loadKeys()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to revoke key')
    }
  }

  const activeKeys = keys.filter(k => !k.revoked_at)
  const revokedKeys = keys.filter(k => !!k.revoked_at)

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900 dark:text-gray-100">API Keys</h1>
          <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Manage API keys for programmatic access</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          <Plus size={16} /> Create API Key
        </button>
      </div>

      {error ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6"><ErrorState message={error} onRetry={loadKeys} /></div>
      ) : loading ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6"><LoadingSkeleton rows={4} columns={3} /></div>
      ) : keys.length === 0 ? (
        <div className="bg-white dark:bg-gray-900 rounded-xl shadow-card mt-6">
          <div className="px-6 py-12 text-center">
            <Key size={32} className="text-gray-300 mx-auto mb-3" />
            <p className="text-sm font-medium text-gray-900 dark:text-gray-100">No API keys</p>
            <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">Create an API key to start using the Velox API</p>
            <button onClick={() => setShowCreate(true)}
              className="mt-4 inline-flex items-center gap-2 px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm transition-colors">
              <Plus size={16} /> Create API Key
            </button>
          </div>
        </div>
      ) : (
        <>
          {/* Active keys */}
          <div className="mt-6 space-y-3">
            {activeKeys.map(k => {
              const isCurrent = currentKeyPrefix && k.key_prefix && currentKeyPrefix.startsWith(k.key_prefix)
              return (
                <div key={k.id} className={`bg-white dark:bg-gray-900 rounded-xl shadow-card px-6 py-4 ${isCurrent ? 'ring-2 ring-velox-200' : ''}`}>
                  <div className="flex items-start justify-between">
                    <div className="flex items-start gap-3">
                      <div className={`w-9 h-9 rounded-lg flex items-center justify-center shrink-0 mt-0.5 ${
                        k.key_type === 'secret' ? 'bg-violet-50' : 'bg-blue-50'
                      }`}>
                        {k.key_type === 'secret' ? <Shield size={16} className="text-violet-500" /> : <Eye size={16} className="text-blue-500" />}
                      </div>
                      <div>
                        <div className="flex items-center gap-2">
                          <p className="text-sm font-medium text-gray-900 dark:text-gray-100">{k.name}</p>
                          {isCurrent && (
                            <span className="text-[10px] font-medium text-velox-700 bg-velox-50 px-1.5 py-0.5 rounded ring-1 ring-velox-200">Current session</span>
                          )}
                        </div>
                        <code className="text-xs font-mono text-gray-400 bg-gray-50 px-2 py-0.5 rounded mt-1 inline-block">{k.key_prefix}••••••••</code>
                        <div className="flex items-center gap-4 mt-2">
                          <Badge status={k.key_type} />
                          <span className="text-xs text-gray-500">Created {relativeTime(k.created_at)}</span>
                          <span className="text-xs text-gray-500">
                            {k.last_used_at ? `Last used ${relativeTime(k.last_used_at)}` : 'Never used'}
                          </span>
                        </div>
                        <p className="text-xs text-gray-500 mt-1.5">
                          {k.key_type === 'secret'
                            ? 'Full access — use server-side only. Never expose in client code.'
                            : 'Read-only access — safe for frontend and client-side use.'}
                        </p>
                      </div>
                    </div>
                    <button onClick={() => {
                      setIsRevokingSelf(!!isCurrent)
                      setRevokeTarget(k)
                    }}
                      className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors shrink-0">
                      Revoke
                    </button>
                  </div>
                </div>
              )
            })}
          </div>

          {/* Revoked keys */}
          {revokedKeys.length > 0 && (
            <div className="mt-6">
              <button onClick={() => setShowRevoked(!showRevoked)}
                className="flex items-center gap-2 text-sm text-gray-600 hover:text-gray-700 transition-colors">
                <ChevronDown size={14} className={`transition-transform ${showRevoked ? 'rotate-180' : ''}`} />
                {revokedKeys.length} revoked key{revokedKeys.length !== 1 ? 's' : ''}
              </button>
              {showRevoked && (
                <div className="mt-3 space-y-2">
                  {revokedKeys.map(k => (
                    <div key={k.id} className="bg-gray-50 rounded-xl px-6 py-3 opacity-60">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-3">
                          <p className="text-sm text-gray-600 line-through">{k.name}</p>
                          <code className="text-xs font-mono text-gray-400">{k.key_prefix}••••</code>
                          <Badge status="revoked" />
                        </div>
                        <span className="text-xs text-gray-500">Revoked {k.revoked_at ? relativeTime(k.revoked_at) : ''}</span>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </>
      )}

      {showCreate && (
        <CreateKeyModal
          onClose={() => setShowCreate(false)}
          onCreated={(rawKey) => {
            setShowCreate(false)
            setCreatedKey(rawKey)
            loadKeys()
            toast.success('API key created')
          }}
        />
      )}

      {createdKey && (
        <Modal open onClose={() => setCreatedKey(null)} title="API Key Created">
          <div className="space-y-4">
            <div className="bg-amber-50 border border-amber-200 rounded-xl p-4">
              <div className="flex items-start gap-3">
                <div className="w-8 h-8 rounded-lg bg-amber-100 flex items-center justify-center shrink-0">
                  <Key size={16} className="text-amber-600" />
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-xs font-semibold text-amber-800">Save this key now — it will not be shown again</p>
                  <div className="flex items-start gap-2 mt-2">
                    <code className="font-mono text-sm text-amber-900 break-all select-all flex-1 bg-amber-100/50 rounded px-2 py-1">{createdKey}</code>
                    <button
                      onClick={() => {
                        navigator.clipboard.writeText(createdKey)
                        toast.success('Copied to clipboard')
                      }}
                      className="shrink-0 px-3 py-1.5 text-xs font-medium text-amber-700 border border-amber-300 rounded-lg hover:bg-amber-100 transition-colors"
                    >
                      Copy
                    </button>
                  </div>
                </div>
              </div>
            </div>
            <div className="flex justify-end pt-2 border-t border-gray-100 dark:border-gray-800">
              <button onClick={() => setCreatedKey(null)}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
                I've saved this key
              </button>
            </div>
          </div>
        </Modal>
      )}

      <ConfirmDialog
        open={!!revokeTarget}
        title={isRevokingSelf ? 'Revoke Current Session Key?' : 'Revoke API Key'}
        message={revokeTarget
          ? isRevokingSelf
            ? 'This is the API key you\'re currently logged in with. Revoking it will log you out immediately. Are you sure?'
            : `Are you sure you want to revoke "${revokeTarget.name}" (${revokeTarget.key_prefix}...)? This action cannot be undone.`
          : ''}
        confirmLabel="Revoke Key"
        variant="danger"
        onConfirm={handleRevoke}
        onCancel={() => { setRevokeTarget(null); setIsRevokingSelf(false) }}
      />
    </Layout>
  )
}

function CreateKeyModal({ onClose, onCreated }: { onClose: () => void; onCreated: (rawKey: string) => void }) {
  const [name, setName] = useState('')
  const [keyType, setKeyType] = useState('secret')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const fieldRules = useMemo(() => ({
    name: [rules.required('Name')],
  }), [])
  const { onBlur, validateAll, fieldError, registerRef } = useFormValidation(fieldRules)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!validateAll({ name })) return
    setSaving(true); setError('')
    try {
      const res = await api.createApiKey({ name, key_type: keyType })
      onCreated(res.raw_key)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create API key')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create API Key" dirty={!!name}>
      <form onSubmit={handleSubmit} noValidate className="space-y-4">
        <FormField label="Name" required value={name} placeholder="e.g. Production, Staging, CI/CD" maxLength={100}
          ref={registerRef('name')} error={fieldError('name')}
          onChange={e => setName(e.target.value)}
          onBlur={() => onBlur('name', name)}
          hint="A descriptive name to identify this key" />

        <div>
          <label className="block text-sm font-medium text-gray-700 mb-2">Key Type</label>
          <div className="grid grid-cols-2 gap-3">
            <button type="button" onClick={() => setKeyType('secret')}
              className={`flex items-start gap-3 p-3 rounded-xl border-2 text-left transition-colors ${
                keyType === 'secret' ? 'border-velox-500 bg-velox-50' : 'border-gray-200 hover:border-gray-300'
              }`}>
              <Shield size={18} className={keyType === 'secret' ? 'text-velox-600 mt-0.5' : 'text-gray-400 mt-0.5'} />
              <div>
                <p className={`text-sm font-medium ${keyType === 'secret' ? 'text-velox-700' : 'text-gray-700'}`}>Secret</p>
                <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Full access. Server-side only.</p>
              </div>
            </button>
            <button type="button" onClick={() => setKeyType('publishable')}
              className={`flex items-start gap-3 p-3 rounded-xl border-2 text-left transition-colors ${
                keyType === 'publishable' ? 'border-velox-500 bg-velox-50' : 'border-gray-200 hover:border-gray-300'
              }`}>
              <Eye size={18} className={keyType === 'publishable' ? 'text-velox-600 mt-0.5' : 'text-gray-400 mt-0.5'} />
              <div>
                <p className={`text-sm font-medium ${keyType === 'publishable' ? 'text-velox-700' : 'text-gray-700'}`}>Publishable</p>
                <p className="text-xs text-gray-500 dark:text-gray-500 mt-0.5">Read-only. Safe for clients.</p>
              </div>
            </button>
          </div>
        </div>

        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 dark:border-gray-800">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 dark:border-gray-600 text-gray-700 dark:text-gray-300 rounded-lg text-sm font-medium hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Key'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
