import { Clock } from 'lucide-react'
import { cn } from '@/lib/utils'
import { DatePicker } from '@/components/ui/date-picker'
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'

// DateTimePicker — branded date+time picker that matches the rest of
// the dashboard's chrome (DatePicker for the date portion, two Select
// dropdowns for hours and minutes). Replaces native <input type="time"
// /> and <input type="datetime-local" />, both of which render OS chrome
// that visually clashes with the rest of the dashboard.
//
// Time is held as the operator-typed string in tenant TZ ("HH:mm").
// Caller is responsible for combining `date` + `time` into a UTC
// timestamp via fromZonedTime — same contract as DatePicker. Keeping
// the picker timezone-agnostic means it stays a pure UI primitive and
// doesn't need to read the tenant TZ itself.
//
// Hour granularity is 1h (24 options); minute granularity is 1min
// (60 options). Test-clock and billing-period flows need minute
// precision so we don't snap to 15-minute increments. Both dropdowns
// scroll if the trigger is near the viewport edge — Select primitive
// handles this.

interface DateTimePickerProps {
  date: string                       // yyyy-MM-dd
  time: string                       // HH:mm (24h, zero-padded)
  onDateChange: (date: string) => void
  onTimeChange: (time: string) => void
  className?: string
  minDate?: Date
  // datePlaceholder is forwarded to DatePicker for the empty-date state.
  datePlaceholder?: string
}

const HOURS = Array.from({ length: 24 }, (_, i) => String(i).padStart(2, '0'))
const MINUTES = Array.from({ length: 60 }, (_, i) => String(i).padStart(2, '0'))

export function DateTimePicker({
  date,
  time,
  onDateChange,
  onTimeChange,
  className,
  minDate,
  datePlaceholder,
}: DateTimePickerProps) {
  const [hh, mm] = (time || '00:00').split(':')
  const setHH = (v: string) => onTimeChange(`${v}:${mm || '00'}`)
  const setMM = (v: string) => onTimeChange(`${hh || '00'}:${v}`)

  return (
    <div className={cn('flex items-center gap-2', className)}>
      <DatePicker
        value={date}
        onChange={onDateChange}
        placeholder={datePlaceholder}
        minDate={minDate}
        className="flex-1 min-w-0"
      />
      <div className="flex items-center gap-1 rounded-lg border border-input bg-transparent dark:bg-input/30 px-2 py-1 h-9">
        <Clock className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
        <Select value={hh} onValueChange={setHH}>
          <SelectTrigger className="h-7 px-1.5 border-0 bg-transparent dark:bg-transparent shadow-none font-mono text-sm">
            <SelectValue placeholder="HH" />
          </SelectTrigger>
          <SelectContent className="max-h-72">
            {HOURS.map(h => (
              <SelectItem key={h} value={h} className="font-mono">{h}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-muted-foreground text-sm">:</span>
        <Select value={mm} onValueChange={setMM}>
          <SelectTrigger className="h-7 px-1.5 border-0 bg-transparent dark:bg-transparent shadow-none font-mono text-sm">
            <SelectValue placeholder="MM" />
          </SelectTrigger>
          <SelectContent className="max-h-72">
            {MINUTES.map(m => (
              <SelectItem key={m} value={m} className="font-mono">{m}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}
