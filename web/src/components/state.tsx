import type { ReactNode } from "react"

import { Button } from "@/components/ui/button"
import { errorMessage } from "@/lib/api"
import { cn } from "@/lib/utils"

/**
 * One surface. Every screen is built from these, so the interface has one
 * kind of container rather than a row of identical cards with shadows.
 */
export function Panel({
  title,
  aside,
  children,
  className,
  bodyClassName,
}: {
  title?: string
  aside?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
}) {
  return (
    <section className={cn("border border-border bg-card rounded-md", className)}>
      {(title || aside) && (
        // Paper is dearer than screen: the padding closes up so an event
        // fits on one sheet.
        <header className="flex items-baseline justify-between gap-4 border-b border-border px-4 py-2.5 print:px-3 print:py-1.5">
          {title && <h2 className="text-sm font-medium text-foreground">{title}</h2>}
          {aside && <div className="text-xs text-muted-foreground">{aside}</div>}
        </header>
      )}
      <div className={cn("p-4 print:p-3", bodyClassName)}>{children}</div>
    </section>
  )
}

/**
 * One labeled reading, for a definition list of them. Every screen states
 * its facts the same way, so a row on System reads like a row on an event.
 */
export function Fact({
  label,
  value,
  note,
  swatch,
  quiet,
}: {
  label: string
  value: ReactNode
  /** A short second line, for a unit or a caveat. */
  note?: ReactNode
  /** A color dot after the value, for a level. */
  swatch?: string
  /** True when the value is the unremarkable one, such as a zero counter. */
  quiet?: boolean
}) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-border py-1.5">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="flex min-w-0 flex-col items-end gap-0.5 text-right">
        <span className={cn("flex min-w-0 items-center gap-2", quiet && "text-muted-foreground")}>
          {value}
          {swatch && (
            <span
              className="size-2 shrink-0 rounded-full"
              style={{ backgroundColor: swatch }}
              aria-hidden
            />
          )}
        </span>
        {note && <span className="text-xs text-muted-foreground">{note}</span>}
      </dd>
    </div>
  )
}

/** A titled list of readings. Every group on a screen states them alike. */
export function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <h3 className="mb-1.5 text-sm font-medium">{title}</h3>
      <dl className="text-sm">{children}</dl>
    </div>
  )
}

/**
 * A failure, in the server's own words. The server writes its errors for a
 * person, so we print the sentence it sent rather than inventing one.
 */
export function ErrorNote({
  error,
  onRetry,
  className,
}: {
  error: unknown
  onRetry?: () => void
  className?: string
}) {
  return (
    <div className={cn("flex flex-col items-start gap-3 py-6 text-sm", className)} role="alert">
      <p className="text-foreground">{errorMessage(error)}</p>
      {onRetry && (
        <Button variant="outline" size="sm" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  )
}

/**
 * Nothing to show, and a sentence saying whether that is good or bad. The
 * action is the way back when the screen is empty because of something the
 * reader did, such as paging past the end of a list.
 */
export function EmptyNote({
  headline,
  detail,
  action,
  className,
}: {
  headline: string
  detail?: string
  action?: { label: string; onClick: () => void }
  className?: string
}) {
  return (
    <div className={cn("py-8 text-sm", className)}>
      <p className="text-foreground">{headline}</p>
      {detail && <p className="mt-1 text-muted-foreground">{detail}</p>}
      {action && (
        <button
          type="button"
          onClick={action.onClick}
          className="mt-2 text-sm text-foreground underline-offset-4 hover:underline"
        >
          {action.label}
        </button>
      )}
    </div>
  )
}

/**
 * Shown only when a request has already been slow. `useResource` holds this
 * back for 300 ms so a fast answer never flashes.
 */
export function LoadingNote({ what, className }: { what: string; className?: string }) {
  return (
    <p className={cn("py-8 text-sm text-muted-foreground", className)} aria-live="polite">
      Reading {what}.
    </p>
  )
}
