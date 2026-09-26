import { Fragment, useEffect, useMemo, useRef, useState, type MouseEvent } from "react"
import { Link, useNavigate } from "react-router-dom"
import { ArrowDownIcon, ArrowUpIcon, ChevronRightIcon, ColumnsIcon } from "lucide-react"
import { flexRender, type ColumnVisibilityState, type SortingState } from "@tanstack/react-table"
import { useTable } from "@tanstack/react-table"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  DEFAULT_HIDDEN,
  eventColumns,
  eventTableFeatures,
  SORTABLE,
} from "@/features/events/columns"
import { ReviewControls, ReviewNote } from "@/features/review/review-controls"
import type { ReviewDesk } from "@/features/review/use-review"
import { MutedMark, ReviewMark } from "@/features/live/event-list"
import { useIsMobile } from "@/hooks/use-mobile"
import type { EventQuery, NoiseEvent } from "@/lib/api"
import { formatDb, levelColor } from "@/lib/level"
import { CLASS_WORD, eventHref, type SortKey } from "@/lib/query"
import { formatDayAndClock, formatDuration } from "@/lib/time"
import { cn } from "@/lib/utils"

export interface TableProps {
  events: NoiseEvent[]
  query: EventQuery
  desk: ReviewDesk
  selectedId: number | null
  onSelect: (id: number) => void
  onSort: (sort: SortKey, order: "asc" | "desc") => void
  /** The ref for the note field, so `n` can put the cursor in it. */
  noteRef: React.Ref<HTMLTextAreaElement>
  /**
   * The events ticked for a bulk action. This is a different thing from
   * `selectedId`, which is where the review keys are: one is a cursor, the
   * other is a set the owner built on purpose.
   */
  ticked: Set<number>
  onTick: (id: number, ticked: boolean) => void
  /** Tick or clear every event on this page. */
  onTickPage: (ticked: boolean) => void
}

/**
 * The event history. One table on a wide screen; one card per event below the
 * `md` breakpoint, because a table with twelve columns is unreadable on a
 * phone and a phone is where a good share of review happens.
 */
export function EventsTable(props: TableProps) {
  const isMobile = useIsMobile()
  const { events, query, desk, selectedId } = props

  const { ticked, onTickPage } = props
  const [columnVisibility, setColumnVisibility] = useState<ColumnVisibilityState>(DEFAULT_HIDDEN)
  const columns = useMemo(() => eventColumns(desk, query), [desk, query])

  const tickedHere = events.filter((event) => ticked.has(event.id)).length
  const pageState: boolean | "indeterminate" =
    tickedHere === 0 ? false : tickedHere === events.length ? true : "indeterminate"

  const sorting: SortingState = [{ id: query.sort ?? "started_ms", desc: query.order !== "asc" }]

  const table = useTable({
    features: eventTableFeatures,
    data: events,
    columns,
    getRowId: (row) => String(row.id),
    // The API sorted and paged the rows. The table must not reorder them.
    manualSorting: true,
    enableSortingRemoval: false,
    state: { sorting, columnVisibility },
    onSortingChange: (updater) => {
      const next = typeof updater === "function" ? updater(sorting) : updater
      const first = next[0]
      if (!first || !SORTABLE.has(first.id)) return
      props.onSort(first.id as SortKey, first.desc ? "desc" : "asc")
    },
    onColumnVisibilityChange: setColumnVisibility,
  })

  // Moving with j and k must bring the row with it. The first selection is
  // not a move, so the page does not jump the moment it loads.
  const moved = useRef(false)
  useEffect(() => {
    if (selectedId === null) return
    if (!moved.current) {
      moved.current = true
      return
    }
    document.getElementById(`event-${selectedId}`)?.scrollIntoView({ block: "nearest" })
  }, [selectedId])

  const selected = events.find((event) => event.id === selectedId) ?? null

  return (
    <div className="flex flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-2">
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <Checkbox
            checked={pageState}
            onCheckedChange={(value) => onTickPage(value === true)}
            aria-label="Select every event on this page"
          />
          <span>
            {events.length === 1 ? "1 event on this page" : `${events.length} events on this page`}
            {tickedHere > 0 && <span className="tabular-nums"> ({tickedHere} selected)</span>}
          </span>
        </label>
        {/* A card shows the same fields every time, so there is nothing to
            choose between on a phone. */}
        {!isMobile && <ColumnMenu table={table} />}
      </div>

      {isMobile ? <EventCards {...props} /> : <EventRows {...props} table={table} />}

      {/* On a wide screen the note has one place at the foot of the list, so
          the rows stay where they are while you move through them. */}
      {!isMobile && selected && (
        <SelectedBar event={selected} desk={desk} query={query} noteRef={props.noteRef} />
      )}
    </div>
  )
}

/** The selected event, its decision, and its note, held at the foot of the list. */
function SelectedBar({
  event,
  desk,
  query,
  noteRef,
}: {
  event: NoiseEvent
  desk: ReviewDesk
  query: EventQuery
  noteRef: React.Ref<HTMLTextAreaElement>
}) {
  return (
    <div className="sticky bottom-0 z-10 border-t border-border bg-card px-4 py-3">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <p className="text-sm">
            {formatDayAndClock(event.started_ms)}
            <span className="ml-3 text-muted-foreground">
              {CLASS_WORD[event.class]}, {formatDb(event.lamax)} dB peak,{" "}
              {formatDuration(event.duration_ms)}
            </span>
          </p>
          <ReviewControls event={event} desk={desk} className="mt-2" />
        </div>
        <div className="flex min-w-64 flex-1 flex-col gap-2">
          <ReviewNote event={event} desk={desk} ref={noteRef} rows={2} />
        </div>
        <Link
          to={eventHref(event.id, query)}
          className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        >
          Open the clip
        </Link>
      </div>
    </div>
  )
}

type TanTable = ReturnType<typeof useTable<typeof eventTableFeatures, NoiseEvent>>

/** Which columns are on screen. It is a view of the same rows, not a filter. */
function ColumnMenu({ table }: { table: TanTable }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="sm" className="-my-1 h-7 gap-1.5 text-xs">
          <ColumnsIcon className="size-3.5" aria-hidden />
          Columns
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-h-80 overflow-y-auto">
        <DropdownMenuLabel>Show these columns</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {table
          .getAllLeafColumns()
          .filter((column) => column.id !== "review")
          .map((column) => (
            <DropdownMenuItem
              key={column.id}
              onSelect={(e) => {
                e.preventDefault()
                column.toggleVisibility()
              }}
              className="gap-2"
            >
              <Checkbox checked={column.getIsVisible()} tabIndex={-1} aria-hidden />
              {column.columnDef.meta?.label ?? column.id}
            </DropdownMenuItem>
          ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function EventRows({
  table,
  desk,
  query,
  selectedId,
  onSelect,
  ticked,
  onTick,
}: TableProps & { table: TanTable }) {
  const navigate = useNavigate()
  return (
    <Table className="border-separate border-spacing-0">
      <TableHeader>
        {table.getHeaderGroups().map((group) => (
          <TableRow key={group.id} className="hover:bg-transparent">
            <TableHead className="h-9 w-8 border-b border-border bg-card">
              <span className="sr-only">Select</span>
            </TableHead>
            {group.headers.map((header) => {
              const column = header.column
              const sortable = SORTABLE.has(column.id)
              const direction = column.getIsSorted()
              return (
                <TableHead
                  key={header.id}
                  aria-sort={
                    !sortable
                      ? undefined
                      : direction === "asc"
                        ? "ascending"
                        : direction === "desc"
                          ? "descending"
                          : "none"
                  }
                  className={cn(
                    "h-9 border-b border-border bg-card text-xs font-medium",
                    column.columnDef.meta?.align === "right" && "text-right",
                  )}
                >
                  {sortable ? (
                    <button
                      type="button"
                      onClick={column.getToggleSortingHandler()}
                      className={cn(
                        "flex items-center gap-1 rounded-xs hover:text-foreground",
                        column.columnDef.meta?.align === "right" && "ml-auto",
                        direction ? "text-foreground" : "text-muted-foreground",
                      )}
                    >
                      {flexRender(column.columnDef.header, header.getContext())}
                      {direction === "asc" ? (
                        <ArrowUpIcon className="size-3" aria-hidden />
                      ) : direction === "desc" ? (
                        <ArrowDownIcon className="size-3" aria-hidden />
                      ) : null}
                    </button>
                  ) : (
                    flexRender(column.columnDef.header, header.getContext())
                  )}
                </TableHead>
              )
            })}
            <TableHead className="h-9 w-8 border-b border-border bg-card">
              <span className="sr-only">Open</span>
            </TableHead>
          </TableRow>
        ))}
      </TableHeader>
      <TableBody>
        {table.getRowModel().rows.map((row) => {
          const event = row.original
          const selected = selectedId === event.id
          return (
            <Fragment key={row.id}>
              <TableRow
                id={`event-${event.id}`}
                aria-selected={selected}
                onClick={(e) => {
                  onSelect(event.id)
                  if (opensEvent(e)) navigate(eventHref(event.id, query))
                }}
                className={cn(
                  // The screen header is 48 px tall and the review bar sits at
                  // the foot, so a row scrolled to stops clear of both.
                  "scroll-mt-16 scroll-mb-32 cursor-pointer border-b border-border",
                  selected && "bg-accent/50 hover:bg-accent/60",
                  desk.settled === event.id && "row-settle",
                )}
              >
                <TableCell className="py-2">
                  <Checkbox
                    checked={ticked.has(event.id)}
                    onCheckedChange={(value) => onTick(event.id, value === true)}
                    aria-label={`Select event ${event.id}`}
                  />
                </TableCell>
                {row.getVisibleCells().map((cell) => (
                  <TableCell
                    key={cell.id}
                    className={cn(
                      "py-2",
                      cell.column.columnDef.meta?.align === "right" && "text-right",
                      selected && "border-l-0",
                    )}
                  >
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </TableCell>
                ))}
                <TableCell className="py-2">
                  <Link
                    to={eventHref(event.id, query)}
                    aria-label={`Open event ${event.id}`}
                    className="inline-flex text-muted-foreground hover:text-foreground"
                  >
                    <ChevronRightIcon className="size-4" aria-hidden />
                  </Link>
                </TableCell>
              </TableRow>
            </Fragment>
          )
        })}
      </TableBody>
    </Table>
  )
}

/**
 * opensEvent decides whether a click on a row should open the event.
 *
 * A row carries the three decision buttons and a link or two, and each of
 * those has its own job. Only the plain parts of a row open the event. A
 * click with a modifier is left alone as well, so the chevron's own link
 * still opens a new tab the way any link does.
 */
function opensEvent(e: MouseEvent): boolean {
  if (e.defaultPrevented || e.button !== 0) return false
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false
  const el = e.target as HTMLElement | null
  return !el?.closest("a, button, input, select, textarea, label, [role='button']")
}

/**
 * One card per event, for a narrow screen. The same facts in reading order,
 * with the three decisions as buttons big enough for a thumb.
 */
function EventCards({
  events,
  query,
  desk,
  selectedId,
  onSelect,
  noteRef,
  ticked,
  onTick,
}: TableProps) {
  const navigate = useNavigate()
  return (
    <ul className="divide-y divide-border">
      {events.map((event) => {
        const review = desk.reviewOf(event)
        const selected = selectedId === event.id
        return (
          <li
            key={event.id}
            id={`event-${event.id}`}
            aria-selected={selected}
            onClick={(e) => {
              onSelect(event.id)
              if (opensEvent(e)) navigate(eventHref(event.id, query))
            }}
            className={cn(
              "scroll-mt-16 cursor-pointer px-4 py-3",
              selected && "bg-accent/50",
              desk.settled === event.id && "row-settle",
            )}
          >
            <div className="flex items-start gap-3">
              <Checkbox
                className="mt-1"
                checked={ticked.has(event.id)}
                onCheckedChange={(value) => onTick(event.id, value === true)}
                aria-label={`Select event ${event.id}`}
              />
              <span
                className="mt-1.5 size-2 shrink-0 rounded-full"
                style={{ backgroundColor: levelColor(event.lamax) }}
                aria-hidden
              />
              <div className="min-w-0 flex-1">
                <span className="flex flex-wrap items-center gap-2">
                  <Link
                    to={eventHref(event.id, query)}
                    className="text-sm underline-offset-4 hover:underline"
                  >
                    {formatDayAndClock(event.started_ms)}
                  </Link>
                  {event.muted && <MutedMark />}
                </span>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  {CLASS_WORD[event.class]} at {(event.confidence * 100).toFixed(0)}% confidence
                </p>
              </div>
              <div className="shrink-0 text-right">
                <p className="text-sm">
                  {formatDb(event.lamax)} <span className="text-xs text-muted-foreground">dB</span>
                </p>
                <p className="text-xs text-muted-foreground">{formatDuration(event.duration_ms)}</p>
              </div>
            </div>

            <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <div className="flex justify-between gap-2">
                <dt>Average</dt>
                <dd className="text-foreground">{formatDb(event.laeq)} dB</dd>
              </div>
              <div className="flex justify-between gap-2">
                <dt>Baseline</dt>
                <dd className="text-foreground">{formatDb(event.baseline_at_trigger)} dB</dd>
              </div>
            </dl>

            <div className="mt-2 flex items-center justify-between gap-3">
              <ReviewMark status={review?.status ?? null} />
              {event.forced && <span className="text-xs text-muted-foreground">Forced close</span>}
            </div>

            <ReviewControls event={event} desk={desk} className="mt-2" />

            {selected && <ReviewNote event={event} desk={desk} ref={noteRef} className="mt-3" />}
            {!selected && review?.note && (
              <p className="mt-2 truncate text-xs text-muted-foreground">{review.note}</p>
            )}
          </li>
        )
      })}
    </ul>
  )
}
