import { forwardRef } from "react"
import { CheckIcon, HelpCircleIcon, Loader2Icon, XIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import type { NoiseEvent, ReviewStatus } from "@/lib/api"
import { cn } from "@/lib/utils"
import type { ReviewDesk } from "@/features/review/use-review"

/** Each decision, as the owner says it, with the key that records it. */
const DECISIONS: { status: ReviewStatus; word: string; key: string; icon: typeof CheckIcon }[] = [
  { status: "verified", word: "Confirm", key: "v", icon: CheckIcon },
  { status: "rejected", word: "Reject", key: "x", icon: XIcon },
  { status: "unsure", word: "Unsure", key: "u", icon: HelpCircleIcon },
]

const ACTIVE_STYLE: Record<ReviewStatus, string> = {
  verified: "border-verified bg-verified/15 text-foreground hover:bg-verified/25",
  rejected: "border-rejected bg-rejected/15 text-foreground hover:bg-rejected/25",
  unsure: "border-unsure bg-unsure/15 text-foreground hover:bg-unsure/25",
}

/**
 * Confirm, reject, or unsure, and a note. The three buttons are the whole job
 * of this product, so they are always reachable by finger as well as by key.
 */
export function ReviewControls({
  event,
  desk,
  compact,
  className,
}: {
  event: NoiseEvent
  desk: ReviewDesk
  /** True in a table row, where there is no space for the words. */
  compact?: boolean
  className?: string
}) {
  const review = desk.reviewOf(event)
  const pending = desk.pendingOf(event.id)
  const failure = desk.failureOf(event.id)

  return (
    <div className={cn("flex flex-wrap items-center gap-1.5", className)}>
      {DECISIONS.map((decision) => {
        const active = review?.status === decision.status
        const busy = pending === decision.status
        return (
          <Button
            key={decision.status}
            variant="outline"
            size="sm"
            aria-pressed={active}
            aria-keyshortcuts={decision.key}
            disabled={pending !== null}
            onClick={() => desk.mark(event, decision.status)}
            className={cn(
              "h-7 gap-1.5 px-2 text-xs",
              active && ACTIVE_STYLE[decision.status],
              compact && "px-1.5",
            )}
            title={`${decision.word} (${decision.key})`}
          >
            {busy ? (
              <Loader2Icon className="size-3.5 animate-spin" aria-hidden />
            ) : (
              <decision.icon className="size-3.5" aria-hidden />
            )}
            <span className={cn(compact && "sr-only")}>{decision.word}</span>
          </Button>
        )
      })}

      {review && (
        <Button
          variant="ghost"
          size="sm"
          aria-keyshortcuts="Backspace"
          disabled={pending !== null}
          onClick={() => desk.clear(event)}
          className="h-7 px-2 text-xs text-muted-foreground"
          title="Clear the decision (Backspace)"
        >
          {pending === "clear" ? (
            <Loader2Icon className="size-3.5 animate-spin" aria-hidden />
          ) : null}
          Clear
        </Button>
      )}

      {pending !== null && <span className="text-xs text-muted-foreground">Saving</span>}
      {pending === null && failure && (
        <span className="text-xs text-rejected" role="alert">
          Not saved: {failure}
        </span>
      )}
    </div>
  )
}

/**
 * The note that goes with the decision. It belongs to the event, so it is
 * saved by pressing a decision, or on its own once a decision exists.
 */
export const ReviewNote = forwardRef<
  HTMLTextAreaElement,
  { event: NoiseEvent; desk: ReviewDesk; rows?: number; className?: string }
>(function ReviewNote({ event, desk, rows = 2, className }, ref) {
  const review = desk.reviewOf(event)
  const changed = desk.noteChanged(event)
  const pending = desk.pendingOf(event.id)

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <label htmlFor={`note-${event.id}`} className="text-xs text-muted-foreground">
        Note
      </label>
      <Textarea
        id={`note-${event.id}`}
        ref={ref}
        rows={rows}
        value={desk.noteOf(event)}
        placeholder="What did you hear?"
        onChange={(e) => desk.setNote(event.id, e.target.value)}
        onKeyDown={(e) => {
          // Escape hands the keyboard back to the review keys.
          if (e.key === "Escape") e.currentTarget.blur()
        }}
        className="resize-y text-sm"
      />
      <div className="flex items-center gap-3">
        {review ? (
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2 text-xs"
            disabled={!changed || pending !== null}
            onClick={() => desk.saveNote(event)}
          >
            Save note
          </Button>
        ) : (
          <p className="text-xs text-muted-foreground">
            The note saves with the decision. Press a decision to keep it.
          </p>
        )}
        {review && changed && <span className="text-xs text-muted-foreground">Not saved yet</span>}
      </div>
    </div>
  )
})
