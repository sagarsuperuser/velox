import type { FieldValues, Path, UseFormReturn } from 'react-hook-form'
import { toast } from 'sonner'
import { ApiError } from './api'

/**
 * Routes a server error onto a react-hook-form instance.
 *
 * - If the server returned a field-scoped error (ApiError.field maps to a form
 *   field), the message is attached to that input via form.setError — the user
 *   sees an inline highlight under the offending field. The input is also
 *   focused so keyboard users land on the problem immediately.
 * - If the error is field-less (InvalidState, NotFound, 500s, unexpected
 *   errors) or the field isn't rendered in this form, the message shows as a
 *   toast instead of silently disappearing.
 *
 * Pass `fields` as either:
 *   - `string[]` of form field names when server and form names match, or
 *   - `Record<string, string>` mapping server-field → form-field when they
 *     differ (e.g. backend `percent_off` maps to form `discountValue`).
 *
 * @example
 * applyApiError(form, err, ['external_id', 'display_name', 'email'])
 *
 * @example
 * applyApiError(form, err, {
 *   percent_off: 'discountValue',
 *   amount_off: 'discountValue',
 *   max_redemptions: 'maxRedemptions',
 * })
 */
export function applyApiError<T extends FieldValues>(
  form: UseFormReturn<T>,
  err: unknown,
  fields: readonly string[] | Record<string, string>,
  opts?: { toastTitle?: string },
): void {
  if (!(err instanceof ApiError)) {
    toast.error(opts?.toastTitle ?? (err instanceof Error ? err.message : 'Something went wrong'))
    return
  }

  const formField = resolveFormField(err.field, fields)
  if (formField) {
    form.setError(formField as Path<T>, {
      type: 'server',
      message: err.message,
    })
    form.setFocus(formField as Path<T>)
    return
  }

  toast.error(err.message)
}

function resolveFormField(
  serverField: string | undefined,
  fields: readonly string[] | Record<string, string>,
): string | undefined {
  if (!serverField) return undefined
  if (Array.isArray(fields)) {
    return fields.includes(serverField) ? serverField : undefined
  }
  return (fields as Record<string, string>)[serverField]
}
