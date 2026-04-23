import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Combobox } from './Combobox'
import { api } from '@/lib/api'

interface CustomerComboboxProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  disabled?: boolean
  clearable?: boolean
  className?: string
}

// Searchable customer picker. Wraps the generic Combobox with the customer
// query + option mapping so every call site doesn't re-derive the same shape.
// Search matches display_name, email, and external_id — operators typically
// type whichever identifier they happen to know.
export function CustomerCombobox({
  value,
  onChange,
  placeholder = 'Select a customer...',
  disabled,
  clearable,
  className,
}: CustomerComboboxProps) {
  const { data, isLoading } = useQuery({
    queryKey: ['customers'],
    queryFn: () => api.listCustomers(),
    staleTime: 30_000,
  })

  const options = useMemo(() => {
    const list = data?.data ?? []
    return list.map(c => {
      const label = c.display_name || c.email || c.external_id || c.id
      return {
        value: c.id,
        label: c.email && c.display_name ? `${c.display_name} — ${c.email}` : label,
        keywords: [c.email, c.external_id, c.display_name, c.id].filter(Boolean) as string[],
      }
    })
  }, [data])

  return (
    <Combobox
      value={value}
      onChange={onChange}
      options={options}
      placeholder={isLoading ? 'Loading customers…' : placeholder}
      emptyMessage="No customers found"
      disabled={disabled}
      clearable={clearable}
      className={className}
    />
  )
}
