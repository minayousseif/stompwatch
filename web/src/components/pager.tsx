import { Button } from "@/components/ui/button"

/**
 * One page back and one page forward, in the words the events list already
 * uses. Newer is on the left because the log reads newest first, so the
 * first page is the newest one and the arrows point the way time runs.
 *
 * The count is a string rather than three numbers, because the two lists
 * count differently. The health log knows its total; the log does not, and
 * a total it invented would be a claim nobody measured.
 */
export function Pager({
  count,
  canNewer,
  canOlder,
  onNewer,
  onOlder,
}: {
  count: string
  canNewer: boolean
  canOlder: boolean
  onNewer: () => void
  onOlder: () => void
}) {
  return (
    <div className="flex flex-wrap items-center justify-end gap-3">
      <p className="text-xs text-muted-foreground">{count}</p>
      <Button
        variant="outline"
        size="sm"
        className="h-7 text-xs"
        disabled={!canNewer}
        onClick={onNewer}
      >
        Newer page
      </Button>
      <Button
        variant="outline"
        size="sm"
        className="h-7 text-xs"
        disabled={!canOlder}
        onClick={onOlder}
      >
        Older page
      </Button>
    </div>
  )
}
