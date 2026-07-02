import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Combobox } from './Combobox'
import { api } from '@/lib/api'
import type { Customer } from '@/lib/api'
import { useDebouncedValue } from '@/hooks/useDebouncedValue'

interface CustomerComboboxProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  disabled?: boolean
  clearable?: boolean
  className?: string
}

function customerOption(c: Customer) {
  const label = c.display_name || c.email || c.external_id || c.id
  return {
    value: c.id,
    label: c.email && c.display_name ? `${c.display_name} — ${c.email}` : label,
  }
}

// Server-searched customer picker. Every keystroke queries the backend's
// `search=` param (name / email / external id, post-decryption match), so
// the picker finds ANY customer — the previous shape fetched one 50-row
// page and filtered client-side, which made every customer past the 50th
// unselectable: on the Subscriptions page that meant their FIRST
// subscription was uncreatable from the dashboard.
export function CustomerCombobox({
  value,
  onChange,
  placeholder = 'Select a customer...',
  disabled,
  clearable,
  className,
}: CustomerComboboxProps) {
  const [search, setSearch] = useState('')
  const debounced = useDebouncedValue(search, 300)

  const { data, isLoading } = useQuery({
    queryKey: ['customers-picker', debounced],
    queryFn: () =>
      api.listCustomers(
        debounced ? `search=${encodeURIComponent(debounced)}&limit=20` : 'limit=20',
      ),
    staleTime: 30_000,
  })

  // The selected customer must stay renderable in the trigger even when
  // the current search results don't include it (picked, then typed a
  // different query). Fetch it by id on demand.
  const results = useMemo(() => data?.data ?? [], [data])
  const selectedInResults = results.some(c => c.id === value)
  const { data: selectedData } = useQuery({
    queryKey: ['customers-picker-selected', value],
    queryFn: () => api.listCustomers(`ids=${value}&limit=1`),
    enabled: !!value && !selectedInResults,
    staleTime: 60_000,
  })

  const options = useMemo(() => {
    const opts = results.map(customerOption)
    if (value && !selectedInResults) {
      const sel = selectedData?.data?.[0]
      if (sel) opts.unshift(customerOption(sel))
    }
    return opts
  }, [results, value, selectedInResults, selectedData])

  return (
    <Combobox
      value={value}
      onChange={onChange}
      options={options}
      placeholder={isLoading ? 'Loading customers…' : placeholder}
      emptyMessage={debounced ? 'No customers match' : 'Type to search customers'}
      disabled={disabled}
      clearable={clearable}
      className={className}
      serverFiltered
      onSearchChange={setSearch}
    />
  )
}
