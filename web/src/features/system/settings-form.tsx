import { useEffect, useState } from "react"
import { toast } from "sonner"

import { ErrorNote, LoadingNote } from "@/components/state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useResource } from "@/hooks/use-resource"
import { api, errorMessage, type Setting } from "@/lib/api"

/**
 * A change to either of these starts the baseline again, which pauses
 * detection until the collector has listened for `min_baseline_s` seconds.
 * An unexplained quiet hour is what this instrument exists to prevent, so
 * the owner is told before the change, not after.
 */
const BASELINE_KEYS = ["baseline_window", "baseline_percentile"]

/** What the owner has typed. A null means "go back to the file default". */
type Pending = Record<string, string | null>

/**
 * The settings the collector will take from here. The form is built from the
 * list the server sends, so a setting added to the collector appears here
 * with no change to this file.
 *
 * `only` and `exclude` split one list between two places on the screen: the
 * camera settings belong beside the camera, and the rest belong under
 * Settings. Each form saves only the fields it shows.
 */
export function SettingsForm({
  only,
  exclude,
  liveTitle,
  liveDetail,
  restartTitle,
  restartDetail,
  offered,
  onSaved,
}: {
  only?: string[]
  exclude?: string[]
  liveTitle?: string
  liveDetail?: string
  restartTitle?: string
  restartDetail?: string
  /**
   * Values something else found and is offering, such as the camera test.
   * They fill the fields in and nothing more: the owner presses save. A
   * test that wrote a setting by itself would take the decision away.
   */
  offered?: Record<string, string> | null
  /** Called after the collector takes a change, so a reading that depends
   *  on these settings can be read again. */
  onSaved?: () => void
} = {}) {
  const resource = useResource((signal) => api.settings(signal), [])
  const [saved, setSaved] = useState<Setting[] | null>(null)
  const [pending, setPending] = useState<Pending>({})
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [confirming, setConfirming] = useState(false)

  // A new object means a new offer, even of the same value, so pressing
  // the button again after an edit fills the field in again.
  useEffect(() => {
    if (offered) setPending((was) => ({ ...was, ...offered }))
  }, [offered])

  const all = saved ?? resource.data?.settings ?? null
  const settings = all
    ? all.filter(
        (one) =>
          (only === undefined || only.includes(one.key)) &&
          (exclude === undefined || !exclude.includes(one.key)),
      )
    : null

  if (resource.error && !settings) {
    return <ErrorNote error={resource.error} onRetry={resource.reload} />
  }
  if (!settings) {
    return resource.loading ? <LoadingNote what="the settings" /> : null
  }

  const changedKeys = settings.filter((one) => changed(one, pending)).map((one) => one.key)
  const baselineTouched = changedKeys.some((key) => BASELINE_KEYS.includes(key))
  const minBaseline = settings.find((one) => one.key === "min_baseline_s")?.value ?? "some"

  const body = () => {
    const out: Record<string, string | null> = {}
    for (const setting of settings) {
      if (!changed(setting, pending)) continue
      const value = pending[setting.key]
      out[setting.key] = value === null ? null : fromInput(setting, value)
    }
    return out
  }

  const save = () => {
    setSaving(true)
    setError(null)
    api
      .saveSettings(body())
      .then((answer) => {
        setSaved(answer.settings)
        setPending({})
        setConfirming(false)
        onSaved?.()
        toast.success(
          changedKeys.length === 1
            ? "The collector took the change."
            : `The collector took all ${changedKeys.length} changes.`,
        )
      })
      .catch((err) => {
        setError(err)
        setConfirming(false)
      })
      .finally(() => setSaving(false))
  }

  const set = (key: string, value: string | null) => setPending((all) => ({ ...all, [key]: value }))

  const undo = (key: string) =>
    setPending((all) => {
      const next = { ...all }
      delete next[key]
      return next
    })

  const live = settings.filter((one) => one.live)
  const onRestart = settings.filter((one) => !one.live)

  return (
    <div className="flex flex-col gap-6">
      <Group
        title={liveTitle ?? "These take effect at once"}
        detail={
          liveDetail ??
          "The running collector applies them as soon as you save. It does not restart."
        }
        settings={live}
        pending={pending}
        onSet={set}
        onUndo={undo}
      />
      <Group
        title={restartTitle ?? "These need a restart"}
        detail={
          restartDetail ??
          "The value is stored now and the collector picks it up the next time it starts."
        }
        settings={onRestart}
        pending={pending}
        onSet={set}
        onUndo={undo}
      />

      {error !== null && (
        <div className="border border-border bg-card p-3 text-sm" role="alert">
          <p>The collector refused the change. It said:</p>
          <p className="mt-1 text-foreground">{errorMessage(error)}</p>
          <p className="mt-2 text-muted-foreground">
            One bad value rejects the whole request, so nothing was saved and the collector is
            running with what it had. Your edits are still in the form. Fix the value it named, or
            discard the changes.
          </p>
        </div>
      )}

      {confirming && (
        <div className="border border-border bg-card p-3 text-sm" role="alert">
          <p className="font-medium">This restarts the baseline.</p>
          <p className="mt-1 max-w-prose text-muted-foreground">
            The baseline is the quiet level the trigger is measured from. Changing how it is worked
            out throws away the one it has, so detection pauses until {minBaseline} seconds of new
            measurement have arrived. The collector keeps measuring and keeps writing seconds
            through that time, but it detects no event, and it records the change in the health log
            so the quiet minutes are explainable.
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <Button size="sm" className="h-8" disabled={saving} onClick={save}>
              Save and start the baseline again
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-8"
              disabled={saving}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <Button
          size="sm"
          className="h-8"
          disabled={changedKeys.length === 0 || saving || confirming}
          onClick={() => (baselineTouched ? setConfirming(true) : save())}
        >
          {saving ? "Saving" : "Save changes"}
        </Button>
        <Button
          variant="outline"
          size="sm"
          className="h-8"
          disabled={changedKeys.length === 0 || saving}
          onClick={() => {
            setPending({})
            setError(null)
            setConfirming(false)
          }}
        >
          Discard
        </Button>
        <p className="text-xs text-muted-foreground">
          {changedKeys.length === 0
            ? "Nothing is changed."
            : `${changedKeys.length} ${changedKeys.length === 1 ? "setting is" : "settings are"} changed and not saved.`}
        </p>
      </div>
    </div>
  )
}

function Group({
  title,
  detail,
  settings,
  pending,
  onSet,
  onUndo,
}: {
  title: string
  detail: string
  settings: Setting[]
  pending: Pending
  onSet: (key: string, value: string | null) => void
  onUndo: (key: string) => void
}) {
  if (settings.length === 0) return null
  return (
    <section>
      <h3 className="text-sm font-medium">{title}</h3>
      <p className="mt-0.5 text-xs text-muted-foreground">{detail}</p>
      <div className="mt-3 grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {settings.map((setting) => (
          <SettingField
            key={setting.key}
            setting={setting}
            pending={pending}
            onSet={onSet}
            onUndo={onUndo}
          />
        ))}
      </div>
    </section>
  )
}

function SettingField({
  setting,
  pending,
  onSet,
  onUndo,
}: {
  setting: Setting
  pending: Pending
  onSet: (key: string, value: string | null) => void
  onUndo: (key: string) => void
}) {
  const id = `setting-${setting.key}`
  const resetting = pending[setting.key] === null
  const dirty = changed(setting, pending)
  const text = shown(setting, pending)

  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id} className="text-xs text-muted-foreground">
        {setting.label}
        {setting.unit ? `, ${setting.unit}` : ""}
      </Label>
      {/* The note is what a range cannot say. It is drawn before the field,
          in the body color rather than gray, so a setting that changes what
          a recording may hold cannot be turned on without reading it. */}
      {setting.note !== "" && (
        <p className="max-w-prose border-l-2 border-border pl-2 text-sm">{setting.note}</p>
      )}
      {setting.type === "bool" ? (
        <label className="flex h-9 items-center gap-2 text-sm">
          <input
            id={id}
            type="checkbox"
            className="size-4 accent-foreground"
            checked={text === "true"}
            aria-describedby={`${id}-note`}
            onChange={(e) => onSet(setting.key, e.target.checked ? "true" : "false")}
          />
          {text === "true" ? "On" : "Off"}
        </label>
      ) : (
        <Input
          id={id}
          className="h-9"
          type={inputType(setting)}
          inputMode={setting.type === "float" ? "decimal" : undefined}
          step={setting.type === "float" ? "any" : undefined}
          min={hasRange(setting) ? setting.min : undefined}
          max={hasRange(setting) ? setting.max : undefined}
          value={text}
          spellCheck={setting.type === "text" ? false : undefined}
          autoCapitalize={setting.type === "text" ? "none" : undefined}
          autoCorrect={setting.type === "text" ? "off" : undefined}
          aria-describedby={`${id}-note`}
          onChange={(e) => onSet(setting.key, e.target.value)}
        />
      )}
      <p id={`${id}-note`} className="text-xs text-muted-foreground">
        {limits(setting)}{" "}
        {resetting ? (
          <>
            Going back to the file default.{" "}
            <button
              type="button"
              onClick={() => onUndo(setting.key)}
              className="text-foreground underline-offset-4 hover:underline"
            >
              Undo
            </button>
          </>
        ) : setting.source === "db" ? (
          <>
            {setting.default === ""
              ? "Overrides the config file, which leaves it empty."
              : `Overrides the config file, which says ${toInput(setting, setting.default)}.`}{" "}
            <button
              type="button"
              onClick={() => onSet(setting.key, null)}
              className="text-foreground underline-offset-4 hover:underline"
            >
              Use the file value
            </button>
          </>
        ) : (
          "From the config file."
        )}
        {dirty && !resetting && <span className="text-foreground"> Changed, not saved.</span>}
      </p>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

/** What the field shows: the edit, the file default, or what is in force. */
function shown(setting: Setting, pending: Pending): string {
  const edit = pending[setting.key]
  if (edit === null) return toInput(setting, setting.default)
  if (edit !== undefined) return edit
  return toInput(setting, setting.value)
}

/** True when this field would change something if it were saved now. */
function changed(setting: Setting, pending: Pending): boolean {
  const edit = pending[setting.key]
  if (edit === undefined) return false
  // Asking for the file default changes nothing when there is no override.
  if (edit === null) return setting.source === "db"
  return edit.trim() !== toInput(setting, setting.value)
}

/**
 * The API value as the field shows it. A duration arrives as Go writes it,
 * `10m0s`, and is shown as the number of seconds its range is counted in.
 */
function toInput(setting: Setting, value: string): string {
  if (setting.type !== "duration") return value
  const seconds = parseDuration(value)
  return seconds === null ? value : String(seconds)
}

/** What the field says, in the form the API takes. */
function fromInput(setting: Setting, text: string): string {
  const trimmed = text.trim()
  if (setting.type !== "duration") return trimmed
  return /^-?\d+(\.\d+)?$/.test(trimmed) ? `${trimmed}s` : trimmed
}

/** A Go duration as a number of seconds. Null when it does not parse. */
function parseDuration(text: string): number | null {
  // Go writes microseconds with the micro sign, and time.ParseDuration also
  // accepts a plain "us". Both spellings are matched by escape, so this file
  // stays ASCII while still reading what the server sends.
  const parts = text.matchAll(/(\d+(?:\.\d+)?)(ms|\u00b5s|us|ns|h|m|s)/g)
  const per: Record<string, number> = {
    ns: 1e-9,
    "\u00b5s": 1e-6,
    us: 1e-6,
    ms: 1e-3,
    s: 1,
    m: 60,
    h: 3600,
  }
  let total = 0
  let found = false
  for (const part of parts) {
    total += Number(part[1]) * per[part[2]]
    found = true
  }
  return found ? Math.round(total * 1000) / 1000 : null
}

function number(value: number | undefined): string {
  return value === undefined ? "-" : String(value)
}

/** True when the setting is a quantity with a range to show and enforce. */
function hasRange(setting: Setting): boolean {
  return setting.type !== "time" && setting.type !== "text" && setting.type !== "bool"
}

function inputType(setting: Setting): string {
  if (setting.type === "time") return "time"
  if (setting.type === "text") return "text"
  return "number"
}

/** The first sentence under a field: what it will accept. */
function limits(setting: Setting): string {
  if (setting.type === "time") return "Local time, as the collector reads the clock."
  if (setting.type === "bool") return "On or off."
  if (setting.type === "text") return "The collector checks this when you save."
  return `${number(setting.min)} to ${number(setting.max)}${setting.unit ? ` ${setting.unit}` : ""}.`
}
