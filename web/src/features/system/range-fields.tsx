import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { fromDateTimeInput, toDateTimeInput } from "@/lib/time"

/**
 * A start and an end, in the browser's time zone. Three sections of the
 * System screen ask for a range, and they all ask for it the same way.
 */
export function RangeFields({
  idPrefix,
  from,
  to,
  onChange,
  fromLabel = "From",
  toLabel = "To",
}: {
  idPrefix: string
  from: number | undefined
  to: number | undefined
  onChange: (from: number | undefined, to: number | undefined) => void
  fromLabel?: string
  toLabel?: string
}) {
  return (
    <>
      <Field label={fromLabel} htmlFor={`${idPrefix}-from`}>
        <Input
          id={`${idPrefix}-from`}
          type="datetime-local"
          className="h-9 w-full sm:w-52"
          value={toDateTimeInput(from)}
          onChange={(e) => onChange(fromDateTimeInput(e.target.value), to)}
        />
      </Field>
      <Field label={toLabel} htmlFor={`${idPrefix}-to`}>
        <Input
          id={`${idPrefix}-to`}
          type="datetime-local"
          className="h-9 w-full sm:w-52"
          value={toDateTimeInput(to)}
          onChange={(e) => onChange(from, fromDateTimeInput(e.target.value))}
        />
      </Field>
    </>
  )
}

export function Field({
  label,
  htmlFor,
  children,
  className,
}: {
  label: string
  htmlFor?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={className ?? "flex min-w-0 flex-col gap-1.5"}>
      <Label htmlFor={htmlFor} className="text-xs text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  )
}
