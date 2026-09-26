import { useCallback, useMemo } from "react"
import { useSearchParams } from "react-router-dom"

import { Screen } from "@/components/app-shell"
import { SiteHeader } from "@/components/site-header"
import { ErrorNote, LoadingNote, Panel } from "@/components/state"
import { Button } from "@/components/ui/button"
import { CameraSection, CAMERA_SETTING_KEYS } from "@/features/system/camera"
import { ExportSection } from "@/features/system/export"
import { HealthLog } from "@/features/system/health-log"
import { LogView } from "@/features/system/log-view"
import { MuteWindows } from "@/features/system/mute-windows"
import { PurgeSection } from "@/features/purge/purge-section"
import { SettingsForm } from "@/features/system/settings-form"
import { CollectingNote, PipelineCounters, SystemFacts } from "@/features/system/status"
import { useInterval } from "@/hooks/use-interval"
import { useResource } from "@/hooks/use-resource"
import { api, type HealthQuery, type LogQuery } from "@/lib/api"
import {
  aroundHealthRow,
  readSystemQuery,
  writeSystemQuery,
  type ExportQuery,
} from "@/lib/system-query"
import { formatClockSeconds } from "@/lib/time"

/** How often the status reads itself again while the screen is open. */
const REFRESH_MS = 30_000

/** The sections, in the order they answer questions. */
const SECTIONS = [
  { id: "health", title: "Health" },
  { id: "logs", title: "Logs" },
  { id: "camera", title: "Camera" },
  { id: "settings", title: "Settings" },
  { id: "mute", title: "Mute windows" },
  { id: "purge", title: "Recordings" },
  { id: "export", title: "Export" },
]

/**
 * One screen, seven sections. Health, logs, the camera, settings, mute
 * windows, the recordings on disk, and export are one destination, because
 * at 2am a second screen is a place to get lost in.
 */
export function SystemScreen() {
  const [search, setSearch] = useSearchParams()
  const query = useMemo(() => readSystemQuery(search), [search])

  const status = useResource((signal) => api.system(signal), [])

  // The status is a reading, so it is re-read while the screen is open.
  useInterval(status.reload, REFRESH_MS)

  const setQuery = useCallback(
    (next: typeof query) => setSearch(writeSystemQuery(next), { replace: true }),
    [setSearch],
  )

  const setHealth = (health: HealthQuery) => setQuery({ ...query, health })
  const setLog = (log: LogQuery) => setQuery({ ...query, log })
  const setExport = (ex: ExportQuery) => setQuery({ ...query, export: ex })

  // A health row says when something happened; the log says what. Following
  // the link narrows the log to a minute either side and moves to it.
  const showLogAt = (tsMs: number) => {
    setQuery(aroundHealthRow(query, tsMs))
    document.getElementById("logs")?.scrollIntoView({ block: "start" })
  }

  return (
    <>
      <SiteHeader
        title="System"
        aside={status.data ? `read at ${formatClockSeconds(status.data.now_ms)}` : undefined}
      />
      <Screen>
        {/* Seven links to seven sections of one screen, not seven
            destinations. */}
        <nav aria-label="Sections of this screen" className="flex flex-wrap gap-2">
          {SECTIONS.map((section) => (
            <Button
              key={section.id}
              asChild
              variant="outline"
              size="sm"
              className="h-8 text-xs font-normal"
            >
              <a href={`#${section.id}`}>{section.title}</a>
            </Button>
          ))}
        </nav>

        <section id="health" className="flex scroll-mt-14 flex-col gap-4">
          <Panel
            title="Health"
            aside={
              <Button
                variant="ghost"
                size="sm"
                className="-my-1 h-7 text-xs"
                onClick={status.reload}
              >
                Refresh
              </Button>
            }
          >
            {status.error ? (
              <ErrorNote error={status.error} onRetry={status.reload} />
            ) : status.data ? (
              <div className="flex flex-col gap-6">
                <CollectingNote status={status.data} />
                <SystemFacts status={status.data} />
              </div>
            ) : status.loading ? (
              <LoadingNote what="the collector's health" />
            ) : null}
          </Panel>

          <Panel title="What the pipeline has lost">
            {status.data ? (
              <PipelineCounters status={status.data} />
            ) : status.loading ? (
              <LoadingNote what="the counters" />
            ) : null}
          </Panel>

          <Panel title="What the collector noticed">
            <HealthLog query={query.health} onChange={setHealth} onShowLogAt={showLogAt} />
          </Panel>
        </section>

        <section id="logs" className="scroll-mt-14">
          <Panel title="Logs">
            <LogView query={query.log} onChange={setLog} />
          </Panel>
        </section>

        <section id="camera" className="flex scroll-mt-14 flex-col gap-4">
          <CameraSection
            status={status.data}
            loading={status.loading}
            error={status.error}
            onRetry={status.reload}
          />
        </section>

        <section id="settings" className="scroll-mt-14">
          <Panel title="Settings">
            {/* The camera settings sit in the camera section above, where
                the status and the test that goes with them are. */}
            <SettingsForm exclude={CAMERA_SETTING_KEYS} />
          </Panel>
        </section>

        <section id="mute" className="scroll-mt-14">
          <Panel title="Mute windows">
            <MuteWindows />
          </Panel>
        </section>

        <section id="purge" className="scroll-mt-14">
          <Panel title="Recordings on disk">
            <PurgeSection />
          </Panel>
        </section>

        <section id="export" className="scroll-mt-14">
          <Panel title="Export">
            <ExportSection query={query.export} onChange={setExport} />
          </Panel>
        </section>
      </Screen>
    </>
  )
}
