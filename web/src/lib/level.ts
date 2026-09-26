// The level ramp: the one place color carries meaning.
//
// The scale is fixed rather than fitted to the data, so a quiet night and a
// loud night draw at the same height and in the same colors. A chart that
// rescales itself makes every night look the same.

export const LEVEL_MIN_DB = 25
export const LEVEL_MAX_DB = 85

/** The ramp, quiet to loud, with the level each color stands for. */
export const LEVEL_RAMP = [
  { db: 25, color: "var(--level-0)" },
  { db: 40, color: "var(--level-1)" },
  { db: 55, color: "var(--level-2)" },
  { db: 68, color: "var(--level-3)" },
  { db: 85, color: "var(--level-4)" },
] as const

/** Where a level sits on the scale, from 0 (quiet) to 1 (loud). */
export function levelFraction(db: number): number {
  const span = LEVEL_MAX_DB - LEVEL_MIN_DB
  return Math.min(1, Math.max(0, (db - LEVEL_MIN_DB) / span))
}

/** The ramp color for one level, for a marker or a dot. */
export function levelColor(db: number): string {
  let chosen: string = LEVEL_RAMP[0].color
  for (const stop of LEVEL_RAMP) {
    if (db >= stop.db) chosen = stop.color
  }
  return chosen
}

/**
 * The ramp as SVG gradient stops down a vertical axis, where offset 0 is the
 * top of the plot. It only lines up because the y axis domain is fixed.
 */
export function verticalRampStops() {
  return LEVEL_RAMP.map((stop) => ({
    offset: `${((1 - levelFraction(stop.db)) * 100).toFixed(1)}%`,
    color: stop.color,
  })).reverse()
}

/** The ramp as a CSS gradient running left to right, for the meter bar. */
export function horizontalRampCss(): string {
  const stops = LEVEL_RAMP.map(
    (stop) => `${stop.color} ${(levelFraction(stop.db) * 100).toFixed(1)}%`,
  )
  return `linear-gradient(to right, ${stops.join(", ")})`
}

/** One decibel reading, always to one place. */
export function formatDb(db: number | null | undefined): string {
  if (db === null || db === undefined || !Number.isFinite(db)) return "-"
  return db.toFixed(1)
}
