import { useEffect } from "react"
import { KeyboardIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"

/**
 * The keys that triage a night. They are the same on the list and on one
 * event, so the hand does not have to relearn the screen.
 */
export const SHORTCUTS: { keys: string[]; what: string }[] = [
  { keys: ["j"], what: "Go to the next event" },
  { keys: ["k"], what: "Go to the previous event" },
  { keys: ["v"], what: "Confirm the event" },
  { keys: ["x"], what: "Reject the event" },
  { keys: ["u"], what: "Mark it unsure" },
  { keys: ["Backspace"], what: "Clear the decision" },
  { keys: ["n"], what: "Write a note" },
  { keys: ["Esc"], what: "Leave the note" },
  { keys: ["Enter"], what: "Open the event, or go back to the list" },
  { keys: ["Space"], what: "Play or pause the clip, on one event" },
  { keys: ["?"], what: "Show these keys" },
]

export interface KeyHandlers {
  next?: () => void
  previous?: () => void
  confirm?: () => void
  reject?: () => void
  unsure?: () => void
  clear?: () => void
  note?: () => void
  open?: () => void
  play?: () => void
  help?: () => void
}

/** True when the keystroke belongs to a field the owner is typing in. */
function isTyping(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  if (target.isContentEditable) return true
  const tag = target.tagName
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT"
}

/**
 * Listen for the review keys. A keystroke in the note field, or with a
 * modifier held, belongs to the field or to the browser and is left alone.
 */
export function useReviewKeys(handlers: KeyHandlers, enabled = true) {
  useEffect(() => {
    if (!enabled) return

    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented) return
      if (event.metaKey || event.ctrlKey || event.altKey) return
      if (isTyping(event.target)) return

      const run = (handler?: () => void) => {
        if (!handler) return
        event.preventDefault()
        handler()
      }

      switch (event.key) {
        case "j":
          return run(handlers.next)
        case "k":
          return run(handlers.previous)
        case "v":
          return run(handlers.confirm)
        case "x":
          return run(handlers.reject)
        case "u":
          return run(handlers.unsure)
        case "n":
          return run(handlers.note)
        case "Backspace":
        case "Delete":
          return run(handlers.clear)
        case "Enter":
          return run(handlers.open)
        case " ":
          return run(handlers.play)
        case "?":
          return run(handlers.help)
      }
    }

    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [enabled, handlers])
}

/** One key, drawn as a key. */
export function Key({ children }: { children: string }) {
  return (
    <kbd className="inline-flex min-w-6 justify-center rounded-xs border border-border bg-muted px-1.5 py-0.5 text-[11px] leading-4 text-foreground">
      {children}
    </kbd>
  )
}

/** The button that opens the key list, so the keys can be found by looking. */
export function ShortcutButton({ onOpen }: { onOpen: () => void }) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="-my-1 h-7 gap-1.5 text-xs"
      onClick={onOpen}
      aria-keyshortcuts="?"
    >
      <KeyboardIcon className="size-3.5" aria-hidden />
      Keys
    </Button>
  )
}

/** The short line under the list, for the keys used most. */
export function ShortcutHint({ onOpen }: { onOpen: () => void }) {
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
      <span className="flex items-center gap-1.5">
        <Key>j</Key>
        <Key>k</Key>
        move
      </span>
      <span className="flex items-center gap-1.5">
        <Key>v</Key>
        confirm
      </span>
      <span className="flex items-center gap-1.5">
        <Key>x</Key>
        reject
      </span>
      <span className="flex items-center gap-1.5">
        <Key>u</Key>
        unsure
      </span>
      <button
        type="button"
        onClick={onOpen}
        className="text-foreground underline-offset-4 hover:underline"
      >
        All keys
      </button>
    </p>
  )
}

/** The whole key list. `?` opens it, which is where people look first. */
export function ShortcutHelp({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full sm:max-w-sm">
        <SheetHeader>
          <SheetTitle>Review keys</SheetTitle>
          <SheetDescription>
            These work on the events list and on one event. They do nothing while you are typing a
            note.
          </SheetDescription>
        </SheetHeader>
        <dl className="divide-y divide-border border-t border-border">
          {SHORTCUTS.map((shortcut) => (
            <div
              key={shortcut.what}
              className="flex items-baseline justify-between gap-4 px-4 py-2.5 text-sm"
            >
              <dt className="flex gap-1.5">
                {shortcut.keys.map((key) => (
                  <Key key={key}>{key}</Key>
                ))}
              </dt>
              <dd className="text-right text-muted-foreground">{shortcut.what}</dd>
            </div>
          ))}
        </dl>
      </SheetContent>
    </Sheet>
  )
}
