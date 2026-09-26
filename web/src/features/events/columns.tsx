import { Link } from "react-router-dom"
import {
  columnVisibilityFeature,
  createColumnHelper,
  rowSortingFeature,
  tableFeatures,
} from "@tanstack/react-table"

import { MutedMark, ReviewMark } from "@/features/live/event-list"
import { ReviewControls } from "@/features/review/review-controls"
import type { ReviewDesk } from "@/features/review/use-review"
import type { EventQuery, NoiseEvent } from "@/lib/api"
import { formatDb, levelColor } from "@/lib/level"
import { eventHref, CLASS_WORD } from "@/lib/query"
import { formatClock, formatDayAndClock, formatDuration } from "@/lib/time"

/**
 * Sorting and paging are the server's job, so the table is told not to touch
 * the rows: `manualSorting` keeps the order the API returned. Column
 * visibility is the table's own, because it is a view, not a query.
 */
export const eventTableFeatures = tableFeatures({
  rowSortingFeature,
  columnVisibilityFeature,
  columnMeta: {} as { align?: "right"; label: string },
})

const helper = createColumnHelper<typeof eventTableFeatures, NoiseEvent>()

/** The columns the API can sort on. Nothing else offers a sort control. */
export const SORTABLE = new Set(["started_ms", "lamax", "duration_ms", "class"])

/** The eight columns worth the width. The rest are there when they are wanted. */
export const DEFAULT_HIDDEN: Record<string, boolean> = {
  id: false,
  ended_ms: false,
  laeq: false,
  low_energy: false,
  high_energy: false,
  low_high_ratio: false,
  forced: false,
  has_audio: false,
  has_video: false,
  reviewer: false,
  reviewed_ms: false,
}

function Number_({ children }: { children: string }) {
  return <span className="tabular-nums">{children}</span>
}

/**
 * The column set. The review column needs the desk and the query, so the
 * columns are built for the screen rather than declared at module level.
 */
export function eventColumns(desk: ReviewDesk, query: EventQuery) {
  return helper.columns([
    helper.accessor("id", {
      header: "Id",
      meta: { label: "Id", align: "right" },
      cell: (info) => <Number_>{String(info.getValue())}</Number_>,
    }),
    helper.accessor("started_ms", {
      header: "Time",
      meta: { label: "Time" },
      cell: (info) => (
        <span className="flex items-center gap-2">
          <Link
            to={eventHref(info.row.original.id, query)}
            className="underline-offset-4 hover:underline focus-visible:underline"
          >
            {formatDayAndClock(info.getValue())}
          </Link>
          {info.row.original.muted && <MutedMark />}
        </span>
      ),
    }),
    helper.accessor("ended_ms", {
      header: "Ended",
      meta: { label: "Ended" },
      cell: (info) => formatClock(info.getValue()),
    }),
    helper.accessor("class", {
      header: "Class",
      meta: { label: "Class" },
      cell: (info) => (
        <span>
          {CLASS_WORD[info.getValue()]}
          <span className="ml-2 text-xs text-muted-foreground">
            {(info.row.original.confidence * 100).toFixed(0)}%
          </span>
        </span>
      ),
    }),
    helper.accessor("lamax", {
      header: "Peak",
      meta: { label: "Peak level", align: "right" },
      cell: (info) => (
        <span className="flex items-center justify-end gap-2">
          <Number_>{formatDb(info.getValue())}</Number_>
          <span
            className="size-2 shrink-0 rounded-full"
            style={{ backgroundColor: levelColor(info.getValue()) }}
            aria-hidden
          />
        </span>
      ),
    }),
    helper.accessor("laeq", {
      header: "Average",
      meta: { label: "Average level", align: "right" },
      cell: (info) => <Number_>{formatDb(info.getValue())}</Number_>,
    }),
    helper.accessor("duration_ms", {
      header: "Length",
      meta: { label: "Length", align: "right" },
      cell: (info) => <Number_>{formatDuration(info.getValue())}</Number_>,
    }),
    helper.accessor("baseline_at_trigger", {
      header: "Baseline",
      meta: { label: "Baseline at trigger", align: "right" },
      cell: (info) => <Number_>{formatDb(info.getValue())}</Number_>,
    }),
    helper.accessor("low_energy", {
      header: "Low",
      meta: { label: "Low-band energy", align: "right" },
      cell: (info) => <Number_>{formatDb(info.getValue())}</Number_>,
    }),
    helper.accessor("high_energy", {
      header: "High",
      meta: { label: "High-band energy", align: "right" },
      cell: (info) => <Number_>{formatDb(info.getValue())}</Number_>,
    }),
    helper.accessor("low_high_ratio", {
      header: "Low minus high",
      meta: { label: "Low minus high", align: "right" },
      cell: (info) => <Number_>{formatDb(info.getValue())}</Number_>,
    }),
    helper.accessor("forced", {
      header: "Forced close",
      meta: { label: "Forced close" },
      cell: (info) => (info.getValue() ? "Yes" : "No"),
    }),
    helper.accessor("has_audio", {
      header: "Clip",
      meta: { label: "Clip" },
      cell: (info) => (info.getValue() ? "Yes" : "No"),
    }),
    helper.accessor("has_video", {
      header: "Video",
      meta: { label: "Video" },
      cell: (info) => (info.getValue() ? "Yes" : "No"),
    }),
    helper.display({
      id: "status",
      header: "Decision",
      meta: { label: "Decision" },
      cell: ({ row }) => {
        const review = desk.reviewOf(row.original)
        return (
          <span className="flex min-w-0 flex-col gap-0.5">
            <ReviewMark status={review?.status ?? null} />
            {review?.note && (
              <span className="max-w-48 truncate text-xs text-muted-foreground">{review.note}</span>
            )}
          </span>
        )
      },
    }),
    helper.accessor((row) => row.review?.reviewer ?? "", {
      id: "reviewer",
      header: "Reviewed by",
      meta: { label: "Reviewed by" },
      cell: (info) => info.getValue() || "-",
    }),
    helper.accessor((row) => row.review?.reviewed_ms ?? 0, {
      id: "reviewed_ms",
      header: "Reviewed",
      meta: { label: "Reviewed at" },
      cell: (info) => (info.getValue() ? formatDayAndClock(info.getValue()) : "-"),
    }),
    helper.display({
      id: "review",
      header: "Review",
      meta: { label: "Review" },
      cell: ({ row }) => <ReviewControls event={row.original} desk={desk} compact />,
    }),
  ])
}
