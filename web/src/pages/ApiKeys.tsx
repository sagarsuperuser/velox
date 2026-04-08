import { useEffect, useState, useMemo } from 'react'
import { api, formatDate, type ApiKeyInfo } from '@/lib/api'
import { Layout } from '@/components/Layout'
import { Badge } from '@/components/Badge'
import { Modal } from '@/components/Modal'
import { FormField, FormSelect } from '@/components/FormField'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { LoadingSkeleton } from '@/components/LoadingSkeleton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { useToast } from '@/components/Toast'
import { useFormValidation, rules } from '@/hooks/useFormValidation'

export function ApiKeysPage() {
  const [keys, setKeys] = useState<ApiKeyInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [createdKey, setCreatedKey] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ApiKeyInfo | null>(null)
  const [isRevokingSelf, setIsRevokingSelf] = useState(false)
  const toast = useToast()

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

  return (
    <Layout>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">API Keys</h1>
          <p className="text-sm text-gray-500 mt-1">Manage API keys for programmatic access</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
          Create API Key
        </button>
      </div>

      <div className="bg-white rounded-xl shadow-card mt-6">
        {error ? <ErrorState message={error} onRetry={loadKeys} />
        : loading ? <LoadingSkeleton rows={5} columns={6} />
        : keys.length === 0 ? <EmptyState title="No API keys" description="Create an API key to start using the Velox API" />
        : (
          <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 bg-gray-50">
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Name</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Prefix</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Type</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Created</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Last Used</th>
                <th className="text-left text-xs font-medium text-gray-500 px-6 py-3">Status</th>
                <th className="text-right text-xs font-medium text-gray-500 px-6 py-3"></th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {keys.map(k => {
                const isRevoked = !!k.revoked_at
                return (
                  <tr key={k.id} className={`hover:bg-gray-50/50 transition-colors ${isRevoked ? 'opacity-50' : ''}`}>
                    <td className={`px-6 py-3 text-sm text-gray-700 ${isRevoked ? 'line-through' : ''}`}>{k.name}</td>
                    <td className="px-6 py-3 text-sm font-mono text-gray-500">{k.key_prefix}...</td>
                    <td className="px-6 py-3"><Badge status={k.key_type} /></td>
                    <td className="px-6 py-3 text-sm text-gray-400">{formatDate(k.created_at)}</td>
                    <td className="px-6 py-3 text-sm text-gray-400">{k.last_used_at ? formatDate(k.last_used_at) : '\u2014'}</td>
                    <td className="px-6 py-3"><Badge status={isRevoked ? 'revoked' : 'active'} /></td>
                    <td className="px-6 py-3 text-right">
                      {!isRevoked && (
                        <button onClick={() => {
                          const currentKey = localStorage.getItem('velox_api_key') || ''
                          setIsRevokingSelf(currentKey.startsWith(k.key_prefix))
                          setRevokeTarget(k)
                        }}
                          className="text-xs font-medium text-red-600 hover:text-red-700 bg-red-50 hover:bg-red-100 px-2.5 py-1 rounded-md transition-colors">Revoke</button>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          </div>
        )}
      </div>

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
          <div className="space-y-3">
            <div className="bg-amber-50 border border-amber-200 rounded-lg p-4">
              <p className="text-xs text-amber-700 font-medium mb-2">Save this key — it will not be shown again.</p>
              <div className="flex items-start gap-2">
                <p className="font-mono text-sm text-amber-900 break-all select-all flex-1">{createdKey}</p>
                <button
                  onClick={() => {
                    navigator.clipboard.writeText(createdKey)
                    toast.success('Copied to clipboard')
                  }}
                  className="shrink-0 px-2 py-1 text-xs font-medium text-amber-700 border border-amber-300 rounded-md hover:bg-amber-100 transition-colors"
                >
                  Copy
                </button>
              </div>
            </div>
            <div className="flex justify-end pt-4 border-t border-gray-100 mt-2">
              <button onClick={() => setCreatedKey(null)}
                className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow transition-colors">
                Done
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
            : `Are you sure you want to revoke the API key "${revokeTarget.name}" (${revokeTarget.key_prefix}...)? This action cannot be undone.`
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
      setError(err instanceof Error ? err.message : 'Failed to create key')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Create API Key">
      <form onSubmit={handleSubmit} noValidate className="space-y-3">
        <FormField label="Name" required value={name} placeholder="Production API Key" maxLength={100}
          ref={registerRef('name')} error={fieldError('name')}
          onChange={e => setName(e.target.value)}
          onBlur={() => onBlur('name', name)} />
        <FormSelect label="Key Type" value={keyType}
          onChange={e => setKeyType(e.target.value)}
          options={[{ value: 'secret', label: 'Secret' }, { value: 'publishable', label: 'Publishable' }]} />
        {error && <p className="text-sm text-red-600 bg-red-50 border border-red-200 rounded-lg px-3 py-2">{error}</p>}
        <div className="flex justify-end gap-3 pt-4 border-t border-gray-100 mt-2">
          <button type="button" onClick={onClose} className="px-4 py-2 border border-gray-300 text-gray-700 rounded-lg text-sm font-medium hover:bg-gray-50 transition-colors">Cancel</button>
          <button type="submit" disabled={saving}
            className="px-4 py-2 bg-velox-600 text-white rounded-lg text-sm font-medium hover:bg-velox-700 shadow-sm hover:shadow disabled:opacity-50">
            {saving ? 'Creating...' : 'Create Key'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
