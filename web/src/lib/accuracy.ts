// How well the levels are known, said in one sentence.
//
// Every level stompwatch reports is dBFS - sensitivity_dbfs + 94, so every one
// of them is only as good as that one constant. An instrument that prints
// 71.8 dB and says nothing about the tolerance is claiming more than it
// knows, and a number later shown to be 2 dB out discredits every other
// number beside it (SPEC.md section 15 decision 23).
//
// The sentence is built from the data every time. It is never a fixed string
// in the interface: a fixed string would go on saying "plus or minus 2 dB"
// the day the owner measures the real sensitivity.

/** The fields the sentence is built from. */
export interface LevelAccuracy {
  /** The plus or minus, in dB. */
  uncertainty_db: number
  /** Where the sensitivity figure came from. */
  source: "datasheet" | "measured"
  /** `YYYY-MM-DD`, or empty for the datasheet figure. */
  measured_on: string
  /** What it was measured against, or empty. */
  reference: string
}

/**
 * The sentence. Two shapes: the manufacturer's figure for the model, or this
 * unit measured against a reference on a named day.
 */
export function accuracySentence(a: LevelAccuracy): string {
  const head = `Levels are accurate to plus or minus ${formatUncertainty(a.uncertainty_db)} dB.`
  if (a.source !== "measured") {
    return (
      `${head} The microphone's sensitivity is the manufacturer's figure for the model, ` +
      `not measured against a reference.`
    )
  }
  const day = formatDay(a.measured_on)
  // A measured figure always carries a day; the collector refuses to start
  // without one. A reference is not required, so the clause that names it is
  // only there when there is one to name.
  const when =
    day === "" ? "The sensitivity was measured" : `The sensitivity was measured on ${day}`
  if (a.reference === "") return `${head} ${when}.`
  return `${head} ${when} against a ${a.reference}.`
}

/**
 * The plus or minus, with no trailing zero: 2, not 2.0, and 0.5 stays 0.5.
 * Two decimals is as fine as the figure is ever stated.
 */
function formatUncertainty(db: number): string {
  if (!Number.isFinite(db)) return "an unrecorded amount of"
  return String(Number(db.toFixed(2)))
}

const MONTHS = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
]

/**
 * `2026-10-03` as `3 October 2026`. The string is read apart rather than put
 * through `Date`, which would shift the day in a browser west of UTC: the
 * date is a calendar day somebody wrote down, not an instant.
 *
 * Anything that is not that shape comes back empty, and the caller leaves
 * the day out rather than printing a date nobody wrote.
 */
export function formatDay(day: string): string {
  const parts = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day)
  if (!parts) return ""
  const month = MONTHS[Number(parts[2]) - 1]
  if (month === undefined) return ""
  return `${Number(parts[3])} ${month} ${Number(parts[1])}`
}
