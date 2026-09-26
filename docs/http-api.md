# HTTP API contract (Phase 3)

This file is the agreement between the Go server and the web interface.
Change it first, then change the code. SPEC.md section 9 is the requirement; this
file is the interface.

## Rules

- Every path starts with `/api`. Everything else serves the embedded web
  interface, with `index.html` returned for unknown paths so client-side
  routes work. `/api` paths that do not exist return 404 JSON.
- All times in requests and responses are **epoch milliseconds** (integer).
  Never RFC 3339, never seconds. The one exception is `samples_1s.ts` inside
  the database itself, which stays in seconds.
- All levels are dB SPL, `REAL`.
- JSON field names are `snake_case` and match the database columns where a
  column exists.
- Errors return `{"error": "what happened and what to do"}` with a 4xx or 5xx
  status. Never return a Go error string that names a filesystem path.
- **No response names a file or a directory anywhere**, not only in errors.
- Responses are not cached: `Cache-Control: no-store` on `/api` paths.
- Read handlers use only the read-only database pool.
- An unknown query parameter, or one whose value is out of range, is a 400
  that names the parameter. Nothing is silently ignored or trimmed.
- A known `/api` path called with the wrong method returns 405 JSON with an
  `Allow` header.
- Every request but `GET /api/live` has a 30-second deadline. The live stream
  is meant to stay open for as long as the browser is watching.
- A `POST`, `PUT` or `DELETE` that a page on another site sent is a 403.
  The browser names the sender in `Sec-Fetch-Site`, or in `Origin` if it is
  older than 2023. Any page the owner opens could otherwise send one, and
  `tailscale serve` would add the owner's identity to it. A request with
  neither header, such as one from curl, goes through.
- A request body must be sent with `Content-Type: application/json`. Any
  other type, or none, is a 415. A form or a no-cors fetch can send
  `text/plain` to another site without asking first; it cannot send JSON.
  With curl, add `-H 'Content-Type: application/json'`.
- Every response carries `X-Frame-Options: DENY`,
  `Content-Security-Policy: frame-ancestors 'none'` and
  `X-Content-Type-Options: nosniff`. No other site may show the dashboard in
  a frame.

## Identity

Middleware puts the caller's identity on the request context. Read it with
`web.Identity(r)`, which returns `(login, name string)`.

| `auth_mode` | Login comes from | Name comes from | Notes |
|---|---|---|---|
| `tailscale` (default) | `auth_header`, default `Tailscale-User-Login` | `auth_name_header`, default `Tailscale-User-Name` | Safe only because the listener is loopback-bound |
| `trusted_header` | `auth_header` | `auth_name_header` | Behind a reverse proxy that authenticates users |
| `none` | the literal string `dev` | `dev` | The process **refuses to start** if `http_addr` is not loopback |

~~A missing header in `tailscale` or `trusted_header` mode gives the login
`unknown`.~~ *Reversed by SPEC.md section 15 decision 27.* In `tailscale` or
`trusted_header` mode an `/api` request with no login header, or an empty
one, is a 401. Only a request that did not come through `tailscale serve` or
the proxy has none, and serving it as `unknown` gave it full rights. The
message says to open the dashboard through `tailscale serve` or the proxy,
or to use `auth_mode = none` on loopback for an SSH tunnel. The page and its
files still load, so the owner can read that message. `tailscale serve` sets
no login for a tagged node, so a tagged node gets the 401 too.

A request that carries `Tailscale-Funnel-Request` is a 403 in every mode,
the page included. `tailscale serve` sets that header on a request that came
from the public internet through Funnel, and removes it from any other
request, so the dashboard never answers the public internet.

## Endpoints

### `GET /api/summary?from=&to=`

```json
{
  "from": 0, "to": 0,
  "events": 12, "reviewed": 4, "verified": 3,
  "loudest": {"id": 91, "lamax": 68.2, "started_ms": 0},
  "laeq": 41.8, "baseline": 33.1,
  "quiet_hour_events": 9, "muted": 2
}
```

`from` and `to` are both required. The range may cover at most 3660 days,
because `quiet_hour_events` is worked out night by night; a longer one is a
400. `loudest` is `null` when the range has no
events, and `laeq` and `baseline` are `null` when the range holds no measured
seconds: a zero there would draw as silence, which is a claim nobody made.

A muted event is left out of `events`, `reviewed`, `verified`,
`quiet_hour_events` and `loudest`, and counted in `muted`. The headline is
about the events the owner has not already explained, and the number of the
rest stays visible rather than being silently dropped. `laeq` and `baseline`
measure the seconds in the range and a mute window does not change them: they
are the measurement, not a count of events.

### `GET /api/timeline?from=&to=&res=auto|1s|1m`

`res=auto` (the default) uses `samples_1s` when `to-from` is 24 hours or less
and `samples_1m` otherwise. A day covers any one night, so the resolution
never changes in the middle of one. Never scan raw seconds for a wider range.

```json
{
  "resolution": "1m",
  "bucket_ms": 60000,
  "from": 0, "to": 0,
  "points": [{"t": 0, "laeq": 41.2, "lamax": 55.0, "baseline": 33.0}],
  "events": [{"id": 1, "started_ms": 0, "ended_ms": 0, "lamax": 60.1,
              "class": "running", "status": "verified", "muted": false}],
  "quiet": [{"from": 0, "to": 0}]
}
```

- For `1m` points, `laeq` is `laeq_mean`, `lamax` is `lamax_max`, and
  `baseline` is omitted (null).
- `quiet` are the quiet-hour spans that overlap the range, from `quiet_start`
  and `quiet_end`, in the server's local time zone.
- `status` is `""` when the event has no review row.
- Cap `points` at 5000. When a range needs more 1-minute rows than that, the
  server **thins them into buckets** rather than refusing: it groups whole
  minutes into the smallest bucket from the list 1, 2, 5, 10, 15, 30, 60, 120,
  240, 360, 720, 1440 minutes that brings the count to 5000 or fewer. This is
  how SPEC.md section 9.1 gets its 7-day and 30-day views without a new rollup table.
  A range longer than 31 days returns 400 and says to narrow it.
- The response reports the bucket with `"bucket_ms"`. It is 60000 when the
  minute rows are returned one for one, and 1000 for `1s`. The bucket follows
  the range asked for, not the rows that came back, so a gap in the data does
  not change how wide a point is. A bucket starts on a whole multiple of its
  own width since the epoch, which for every width on the list is also a
  whole minute of the clock.
- Inside a bucket, `laeq` is the **energy mean** of the minute means weighted
  by each minute's `n`, `lamax` is the largest `lamax_max`, and `baseline` is
  null. A bucket with no rows is left out; the trace must show a gap in the
  measurement, not a straight line across it.
- **Seconds are thinned the same way.** A day is 86 400 seconds, far past the
  cap, so seconds are grouped into the smallest bucket from 1, 2, 5, 10, 15,
  30, 60, 120, 300 seconds that brings the count to 5000 or fewer. A thinned
  second bucket **keeps its baseline**, as the energy mean of the seconds in
  it, because the baseline is the line the trigger level is measured from and
  the most useful context on the trace. The minute rollup stores no baseline,
  which is why the seconds are thinned rather than handed over to it.
- `res=1s` covers at most 24 hours, the same bound `auto` uses. A wider range
  returns 400 and says to ask for `res=1m`.

### `GET /api/events`

Query parameters, all optional:

| Name | Meaning |
|---|---|
| `from`, `to` | epoch ms, filter on `started_ms` |
| `class` | repeatable: `running`, `jumping`, `stomping`, `impact`, `steady`, `airborne`, `unknown` |
| `status` | repeatable: `verified`, `rejected`, `unsure`, `none` |
| `min_lamax`, `max_lamax` | dB SPL |
| `min_duration_ms` | integer |
| `quiet_only` | `1` to keep only events that start inside quiet hours. With both `from` and `to` given, the range may cover at most 3660 days; a longer one is a 400 |
| `muted` | `1` keeps only muted events, `0` only unmuted ones; absent keeps both |
| `q` | substring of the review note, case-insensitive |
| `sort` | `started_ms` (default), `lamax`, `duration_ms`, `class` |
| `order` | `desc` (default) or `asc` |
| `limit` | 1 to 500, default 100 |
| `offset` | default 0 |

```json
{
  "total": 231,
  "events": [{
    "id": 1, "started_ms": 0, "ended_ms": 0, "duration_ms": 1200,
    "laeq": 52.1, "lamax": 60.3, "baseline_at_trigger": 33.0,
    "low_energy": 55.0, "high_energy": 40.0, "low_high_ratio": 15.0,
    "class": "running", "confidence": 0.8, "forced": false,
    "jump_db": 17.1, "rise_db": 13.3,
    "has_audio": true, "has_video": false, "muted": false,
    "review": {"status": "verified", "note": "", "reviewer": "me@example.com",
               "reviewed_ms": 0}
  }]
}
```

`review` is `null` when there is no review row. `total` is the count before
`limit` and `offset`.

- `class` is `impact` for one short hit, `steady` for low-frequency sound
  that was already there or rose slowly (a fan starting), `airborne` for
  sound whose high band is as loud as its low band (a television, a voice).
  The other four keep their meaning. Every event is stored whatever its
  class; the class only says what the detector thinks.
- `jump_db` is the peak over the median level of the 30 s before the event.
  `rise_db` is the largest step between two consecutive one-second means,
  from one second before the event to its close. Both are `null` for events
  recorded before schema version 7 and for events with less than 10 s of
  levels before them. Null means "not measured", never 0.

`muted` is true when the event's `[started_ms, ended_ms]` overlaps a mute
window. Both edges count: an event that ends exactly when a window opens, or
starts exactly when one closes, is muted. An event covered by two windows is
still one row. The flag is worked out in SQL as part of the same query, so
`total`, `limit` and `offset` always describe the rows that came back, and
the windows are read on every request because the owner edits them while the
server runs.

### `GET /api/events/{id}`

The event object above, plus:

```json
{
  "media": [{"kind": "audio", "bytes": 30044, "duration_ms": 15000,
             "sha256": "...", "start_ms": 0, "missing_head_ms": 0,
             "rate_hz": 1000, "truncated": false, "missing": false,
             "purged_ms": null, "purged_by": null}],
  "envelope": {"rate_hz": 100, "start_ms": 0, "values": [0.01, 0.4]},
  "samples": [{"t": 0, "laeq": 41.0, "lamax": 55.0, "baseline": 33.0}],
  "capture": {"from_ms": 0, "sensitivity_dbfs": -13.0, "uncertainty_db": 2.0,
              "source": "datasheet", "measured_on": "", "reference": "",
              "calibration": "", "calibration_offset_db": 0,
              "device": "hw:EM01,0", "channel": 0}
}
```

- `capture` is the capture and calibration settings **in force when this
  event was recorded**, read from `capture_settings` (schema version 6). It
  is **not** what is in force now: reprinting an event from three months ago
  must not claim today's calibration.
- `capture` is **null** when the history does not reach back to this event,
  which is every event recorded before schema version 6. The interface must
  say the settings in force at capture were not recorded. It must never fall
  back to `GET /api/system`, which answers for today.
- `uncertainty_db` is the plus or minus on every level of this event, in dB.
  `source` is `datasheet` (the manufacturer's figure for the model) or
  `measured` (this unit, against a reference). `measured_on` is `YYYY-MM-DD`
  and `reference` is free text; both are empty strings for the datasheet
  figure, which was not measured on a day and not measured against anything.
- `calibration` is the **base name** of the calibration file, never its path,
  and an empty string when none was configured. `calibration_offset_db` is
  the broadband correction it gave.

- `media` never exposes the file path. `missing` is true when the file has
  **vanished**: the row exists, no purge was recorded, and the file is not
  there. That is a fault, so the handler also writes a `media_missing`
  `system_health` row, at most once per event per process run.
- `purged_ms` and `purged_by` say the file was deleted on purpose, by whom
  and when, from `media_purge`. `purged_by` is a login for the owner's
  purge and `retention` for the retention job. Both are `null` for a clip
  that was never purged. A purged clip has `missing` **false**: it is not a fault, and no
  `media_missing` row is ever written for it. `bytes`, `duration_ms` and
  `sha256` keep the values the collector recorded, because the row is the
  record that the clip existed and what it was.
- `start_ms` is the time of the clip's **first sample**, from
  `event_media.started_ms`. It is **null** for a clip stored before schema
  version 2 recorded it. Null means "not recorded"; it never means the epoch,
  and the interface must not fall back to a start worked out from the current
  `pre_roll_s`, which may not be the value that clip was written under.
- `missing_head_ms` is how much of the requested pre-roll the clip does not
  have: `start_ms - (event.started_ms - pre_roll_s x 1000)`, floored at 0. It
  is 0 when the whole pre-roll is there and **null** whenever `start_ms` is
  null, because it is only ever computed from a recorded start. `truncated`
  says a clip is short; this says by how much, so a 62 ms shortfall and a
  three second one do not look the same.
- `clipped` counts samples that hit the 16-bit limit. Above zero, the clip
  is distorted at those moments and no playback setting can undo it.
- `rate_hz` is the rate of the stored file, read from the file itself. The
  clip filter cutoff is **half** it, so a 1000 Hz clip was filtered at
  500 Hz. It is `null` when the file is gone. A clip keeps the rate it was
  recorded at, so a database holds clips of more than one rate whenever
  `clip_lowpass_hz` has been changed. Never assume 1 kHz.
- `peak_dbfs` is the loudest sample of the stored clip, and
  `playback_gain_db` is what `GET /api/events/{id}/audio` applies at the
  default tone, which may be negative. Both are `null` when the file is
  gone. The two are not simply related, because the gain is worked out
  after the shelf and the interpolation.
- `envelope.values` are the stored linear envelope samples, not dB. The web
  interface converts them.
- `samples` are `samples_1s` rows from 30 s before the event to 30 s after.
- A `media` entry with `"kind": "video"` is the camera clip (Phase 2). It
  has the same fields. `start_ms` is the start of the first ring segment in
  the clip, which is **at or before** the start of the pre-roll: clip edges
  land on segment edges and the clip is padded outward, never trimmed. So
  `missing_head_ms` is 0 unless the ring did not reach back far enough.
  `truncated` is true when the segments do not hold the whole window,
  because the ring was short, the last segment was still open, or the
  camera was away for part of it; the health log has a `clip_truncated`
  row saying which. `clipped` is 0 and `rate_hz`, `peak_dbfs`, and
  `playback_gain_db` are `null`: they are properties of audio.
- `camera_audio` is true when the clip file really carries the camera's own
  audio track, so it may hold intelligible speech. The collector reads the
  written file back to find out, so this is the clip's own answer and not the
  setting: a clip cut with the setting on that came out with no usable audio
  track is false here, and the health log has a `clip_no_audio` row saying
  why. It is false for every clip cut with the setting off, which is the
  default, and for every audio clip: an audio clip is the measuring
  microphone's, filtered at `clip_lowpass_hz`
  (SPEC.md section 15 decision 22). A clip keeps what it was recorded with,
  so a database holds clips of both kinds.

### `PUT /api/events/{id}/review`

Body: `{"status": "verified", "note": "kids again"}`. `status` must be
`verified`, `rejected`, or `unsure`. `reviewer` comes from the identity, never
from the body. Returns the stored review object. 404 if the event does not
exist.

### `DELETE /api/events/{id}/review`

Removes the review row so the event is unreviewed again. Returns 204, or 404
if the event does not exist: a mistyped number must not answer "done".
`event_review` is the one table that is not immutable.

### `GET /api/events/{id}/audio?tone=tilt|flat`

The clip, **resampled for playback**. Browsers refuse to play below 3 kHz, so
the server raises the clip to at least 8 kHz and serves 16-bit mono WAV. The
factor is the smallest power of two that gets there, chosen per clip from
the rate in the file: 8 for a 1 kHz clip, 4 for 2 kHz, 2 for 4 kHz, and 1
for 8 kHz and above. This adds nothing above the clip's own cutoff that was
not already there; it only makes the file playable.

The interpolation is a 12-tap Blackman-windowed sinc, not the straight line
this file first called for. Linear interpolation was built and measured: it
left the images of the signal only **23 dB** below it, which puts energy back
above the cutoff that the collector filtered out before storing anything. The
windowed sinc holds them **84 dB** down, measured with a DFT of the bytes the
server actually sends. The measurement is the test, not the assumption.

**The playback stream is also turned up.** A household impact is recorded
tens of dB below full scale - a real clip measured -38.1 dBFS - so a faithful
copy is close to inaudible on a phone. The server scales the clip so its peak
reaches -1 dBFS, at most +40 dB, and reports the figure as
`playback_gain_db`. The 1 dB of headroom absorbs the interpolation
overshooting between two stored samples. The stored file and its hash never
change, and the dB trace keeps the measured level, so loudness comparison
stays where it is measured.

**It is also shaped for a small speaker.** Most of a clip's energy sits
below 80 Hz, which a phone speaker cannot reproduce at all. `tone=tilt`, the
default, applies a high shelf at 200 Hz of +12 dB, at the clip's own rate, so
the part a small speaker can make dominates. `tone=flat` keeps the balance
the microphone recorded. Neither adds anything that was not recorded, and
neither touches the stored file. Any other value is a 400.

The level is measured on the finished signal, after the shelf and after the
interpolation, and the gain goes **down** as well as up. Measuring the
stored samples instead would miss the shelf ringing at the abrupt start of
an extract and the interpolation overshooting a sharp edge, and a loud clip
lifted 12 dB would come out with the harmonics of a squared-off sine in it.

Served with `http.ServeContent` so range requests work. Without them a browser
downloads the whole clip before it plays a note, and reviewing a five minute
event is unusable. The resampled bytes are built once and cached in memory,
keyed by event id together with the file's modification time, capped at
32 clips **and** at 64 MiB: a forced event can run for five minutes, so a
count on its own is not a bound on memory. 404 if the file is missing, and
record `media_missing`, at most once per event per process run.

**410 for a purged clip.** When the media row exists and `media_purge` has a
row for it, the answer is 410 Gone with a message saying the recording was
deleted, when, and by whom: a login, or `retention` when the retention job
took it. Not 404, which means "never heard of it", and no `media_missing`
health row: the file was deleted on purpose and the health log must not
fill with alarms about it.

### `GET /api/events/{id}/audio/original`

The stored file, byte for byte, at the rate it was recorded at, with
`Content-Disposition: attachment; filename="event-<id>.wav"`. The handler
recomputes the SHA-256 and compares it with `event_media.sha256`. On a
mismatch it returns 409 and records a `write_error` health row, because a clip
that no longer matches its hash cannot be used as evidence.

The whole file is hashed **before** any of the body goes out. A mismatch found
half way through a response cannot be reported: the status line has already
said 200 and the browser has already saved part of the file.

A purged clip answers 410 Gone here too, and writes no health row.

### `GET /api/events/{id}/video`

The stored video clip, byte for byte, as `video/mp4`. Served with
`http.ServeContent` so range requests work: a browser fetches a video in
many ranges and seeks by asking for another one. The path comes from
`event_media.path` and is refused, with a 500 that names no path, if it
leads outside `video_dir`; the same check the audio handlers make against
`clip_dir`. 404 if there is no video row for the event, and 404 with a
`media_missing` health row, at most once per event per process run, if the
row exists but the file is gone. 410 Gone, with no health row, when the file
was purged: see `POST /api/media/purge`.

The hash is **not** recomputed on each request. A range request would
otherwise hash the whole file every time the browser asked for the next
second. `event_media.sha256` and the `sha256` field of the media entry are
there to check the file by hand: `sha256sum` on the box gives the same hex.

### `GET /api/events/{id}/waveform?buckets=N`

`N` is 100 to 4000, default 800. Peaks computed from the stored clip, at the
rate it was recorded at.

```json
{"buckets": 800, "duration_ms": 15000, "peaks": [[-0.2, 0.3]]}
```

Each peak is `[min, max]` in the range -1 to 1, rounded to four decimal
places so a 4000-bucket response stays small. A clip with fewer stored samples
than buckets gives one bucket per sample, and `buckets` in the response says
how many came back rather than how many were asked for. `duration_ms` is the
length of the stored samples the peaks were taken from.

### `POST /api/media/purge`

Deletes the clip **files** of the events named, and nothing else. It is the
only endpoint that removes anything from disk. The retention job in the
collector is the other thing that does, after `retention_days`, and it
records its deletions the same way with `purged_by` = `retention`.

```json
{"event_ids": [1, 2, 3], "kinds": ["audio", "video"], "confirm": true}
```

- `event_ids` is 1 to 500 whole numbers above zero. More than 500 is a 400:
  a request that takes minutes and cannot be described in one sentence is
  not a request the owner meant to make. Repeated numbers count once.
- `kinds` is optional. Absent means both kinds. Any value but `audio` and
  `video` is a 400.
- `confirm` must be `true`. It is the confirmation step, carried in the
  request rather than left to the interface, so a stray POST from a script
  or a mistyped fetch cannot delete a recording. The web interface sets it
  only from the confirm button of the confirmation panel.

**What is deleted and what is not.** Only the file. The event row, the
second-by-second samples, the review decision and its note, and the
`event_media` row with its `bytes`, `duration_ms` and `sha256` all stay
exactly as they were. `event_media` is the record that the clip existed and
what it was; deleting it would change what the database claims (SPEC.md
section 3 rule 3 and section 15 decision 21).

Each deleted file gets a row in `media_purge`, which carries the event, the
kind, the bytes reclaimed, the time, and the login from `web.Identity(r)`.
`media_purge` has the same immutability triggers as the other raw tables:
once a purge is recorded it cannot be unrecorded. The whole request also
writes exactly one `media_purge` row in `system_health`, naming the events
and the bytes, because rule 3 says every deletion is logged.

```json
{
  "purged_files": 2,
  "purged_bytes": 34568,
  "events": [
    {"event_id": 1, "bytes": 34568,
     "results": [{"kind": "audio", "outcome": "purged", "bytes": 34568},
                 {"kind": "video", "outcome": "no_media", "bytes": 0}]}
  ],
  "unknown_events": [99]
}
```

`outcome` is one of:

| `outcome` | What happened |
|---|---|
| `purged` | The file was deleted and the purge recorded. `bytes` is what it took. |
| `already_gone` | There was a media row but no file. The purge is recorded with `bytes` 0, so the record still says the clip is deliberately gone. |
| `already_purged` | A purge row was already there. Nothing is written twice. |
| `no_media` | The event has no clip of that kind. Nothing is written. |
| `refused` | The stored path leads outside the media root for its kind, so it is **not** deleted. `reason` says so without naming the path. |

`unknown_events` lists ids with no event row. Nothing is deleted, resolved,
or recorded for them: an id that is not an event must never reach a
filesystem path. They are reported rather than failing the request, so one
stale id in a list of fifty does not lose the other forty-nine.

The path of each file is resolved with the same containment check the clip
handlers use, against `clip_dir` for audio and `video_dir` for video. A row
whose path escapes its root is refused, never deleted.

Purging the same events twice is safe. The second request reports
`already_purged` for every one and deletes nothing.

### `GET /api/media/usage?started_before=&event_id=`

How much space the clip files take, so the interface can say what a purge
will reclaim before the owner presses anything.

```json
{
  "audio": {"files": 118, "events": 118, "bytes": 3600000},
  "video": {"files": 40, "events": 40, "bytes": 900000000},
  "purged": {"files": 5, "events": 4, "bytes": 150000},
  "purgeable": {"files": 158, "events": 150, "bytes": 903600000},
  "started_before": 0
}
```

- `audio` and `video` are the files still held: rows with no purge row. The
  figures come from `event_media.bytes`, which is what the collector wrote,
  not from a walk of the disk.
- `purged` is what has already been reclaimed, from `media_purge`.
- `purgeable` is what a purge would reclaim now. With no filter it is
  `audio` plus `video`. `started_before`, an epoch millisecond, counts only
  events that started before that instant, which is what "recordings older
  than 30 days" asks. `event_id` is repeatable, at most 500 times, and
  counts only those events, which is what a selection asks: it is how the
  Events screen names the exact megabytes in its confirmation. Both
  narrow `purgeable` only; the held and purged figures are always the whole
  store.
- `events` is how many distinct events the files belong to, because the
  interface counts events and the owner deletes by event.

### `GET /api/media/purgeable?started_before=&limit=`

The events that still hold a clip and started before an instant, oldest
first. `limit` is 1 to 500, default 500.

```json
{"event_ids": [12, 15, 19], "more": false}
```

Purging by age is two steps on purpose. This endpoint says which events
match; `POST /api/media/purge` then deletes exactly those. Nothing is
deleted that the owner was not shown the count of first, and the age is
resolved once rather than twice, so an event that arrives between the two
steps cannot be swept up.

`more` is true when the filter matches more events than `limit` returned.
The interface says so rather than quietly deleting part of what it showed.
Oldest first means a limit takes the oldest recordings rather than an
arbitrary handful.

### `GET /api/live`

Server-sent events. `Content-Type: text/event-stream`,
`Cache-Control: no-cache`, `X-Accel-Buffering: no`, and a flush after every
write. One `data:` line per second:

```json
{"t": 0, "laeq": 41.2, "lamax": 55.0, "baseline": 33.0, "stale": false}
```

A stale line repeats the last reading with `stale: true`, so the interface can
gray out a number the owner can still read. Before the first second has been
measured there is no reading, and `laeq`, `lamax` and `baseline` are `null`.

`stale` is true when no new bin arrived for 5 s, so the interface can show
that measurement has stopped rather than freezing on an old number. A comment
line `:keepalive` goes out every 15 s. The stream ends when the client
disconnects or the process shuts down.

The source is a `web.LiveFeed` that the pipeline publishes each `meter.Bin`
to. It must never block the DSP goroutine: publishing to a slow subscriber
drops the update for that subscriber only.

### `GET /api/health?from=&to=&kind=&limit=&offset=`

`kind` is repeatable and must name a kind the collector writes. `limit` 1 to
500, default 100, newest first. `offset` pages through the rest.

```json
{"total": 9, "records": [{"id": 1, "ts_ms": 0, "kind": "capture_gap",
                          "detail": "...", "duration_ms": 1200}]}
```

### `GET /api/system`

```json
{
  "started_ms": 0, "now_ms": 0,
  "last_audio_ms": 0, "last_commit_ms": 0, "collecting": true,
  "recording_pause": {"span": "19:00-23:00", "active": false, "until": ""},
  "db_bytes": 0, "wal_bytes": 0, "schema_version": 7,
  "disk": [{"name": "database", "free_mb": 870000},
           {"name": "clips", "free_mb": 870000}],
  "clips": {"count": 120, "bytes": 3000000},
  "counts": {"events_today": 3, "events_total": 210, "unreviewed": 12,
             "samples_today": 40000},
  "capture": {"device": "hw:EM01,0", "channel": 0, "gain": "no capture control",
              "sensitivity_dbfs": -13.0, "calibration": "em01.txt (correction 0.50 dB)"},
  "levels": {"uncertainty_db": 2.0, "source": "datasheet",
             "measured_on": "", "reference": ""},
  "pipeline": {"chunks": 0, "dropped_samples": 0, "bins_dropped": 0,
               "events_dropped": 0, "write_failures": 0, "loop_restarts": 0},
  "heartbeat": {"configured": false, "interval_ms": 300000},
  "auth": {"mode": "tailscale", "login": "me@example.com", "name": "Me"},
  "camera": {"enabled": true, "connected": true, "connected_since_ms": 0,
             "uptime_ms": 0, "disconnects": 2, "last_segment_ms": 0,
             "ring": {"segments": 60, "bytes": 90000000},
             "clips": {"count": 12, "bytes": 30000000},
             "config": {"host": "192.168.1.40", "port": 554,
                        "sub_path": "Preview_01_sub",
                        "main_path": "Preview_01_main",
                        "sub_path_source": "configured",
                        "ring_minutes": 10, "segment_seconds": 10,
                        "audio": false,
                        "audio_track": {"known": true, "present": true,
                                        "codec": "aac", "rate_hz": 16000},
                        "ffmpeg": "ffmpeg version 7.1",
                        "credentials": {"loaded": true, "source": "file",
                                        "user": "admin"}}},
  "tailscale": {"installed": true, "running": true, "backend": "Running",
                "name": "stompwatch.example.ts.net",
                "serving": true,
                "serve_url": "https://stompwatch.example.ts.net/",
                "funnel": false, "key_expiry_ms": 0, "err": "",
                "http_addr": "127.0.0.1:8080"}
}
```

`recording_pause` is the daily pause. `span` lists the windows,
`HH:MM-HH:MM` separated by commas, in local time, or is empty when there is
no pause. `active` says whether `now_ms` is inside one, and `until` is the
end of that window, `HH:MM`, or empty. Inside it no event opens and no audio
clip or camera video is recorded; `collecting` stays true, because the level
of every second is still measured.

`collecting` uses the same rule as the heartbeat: audio within 10 s and bins
within `batch_interval x heartbeat_stall_factor`.

`levels` is how well the levels are known **now**, which is the right thing
for a System screen. Every level rests on `capture.sensitivity_dbfs`, so
`uncertainty_db` is the plus or minus on every level the instrument reports.
`source` is `datasheet` or `measured`; `measured_on` and `reference` are empty
strings for the datasheet figure. An **event** must not read its uncertainty
from here: it cites its own `capture` block, which is the settings in force
when it was recorded.

`capture.calibration` names the calibration file by its **base name** and
never by its path, followed by the broadband correction it gave, or `none`.

`disk` says which store is filling up, not where it is. The entries are
`database` and `clips`, and `video` when a camera is configured; they may
sit on different filesystems. A response never names a directory.

`camera` is the video side (Phase 2, SPEC.md section 7). `enabled` is false when
`camera_host` is empty, and then every other field is zero. `connected` is
true while ffmpeg is running and segments are growing. `uptime_ms` is the
time the camera has **actually delivered video** since the collector
started, not the time since it started, so a gap in coverage shows as
uptime short of the process uptime. `disconnects` counts the times the
stream ended or stalled; each one is a `camera_disconnect` row in the
health log with ffmpeg's reason, and a second row with the length of the
gap when video resumes. `ring` is what the segment ring holds now, and
`clips` counts the event clips on disk.

`camera.config` is how the camera is set up. It is present whether or not
`camera_host` is set, because the owner reads it while bringing a camera
up. `sub_path_source` is `configured` when `camera_rtsp_path` names a path,
`discovered` when the collector found one by trying the known cameras in
order, and `none` when there is no path at all yet; a discovered path is
tried again on every reconnect, so the owner needs to know which it is.
`sub_path` and `main_path` may carry a query, as an Amcrest or Dahua camera
does: `cam/realmonitor?channel=1&subtype=1`. `main_path` is the main path of
the camera whose sub path is in force, or empty when `camera_rtsp_path_main`
is unset and the sub path is not one of the known cameras. `ffmpeg` is the version line of the
installed ffmpeg, or empty when it is not installed.

`camera.config.audio` is the `camera_audio` setting: true means the ring and
the event clips carry the camera's own audio track, unfiltered, and hold
intelligible speech. False is the default (SPEC.md section 15 decision 22).

`camera.config.audio_track` is what the camera actually sends. `known` is
false until something has asked the camera, and then `present` is false
because nothing has been measured, not because the camera is silent. The
collector asks once at start when `camera_audio` is on; `POST
/api/camera/probe` asks whenever the owner presses the button, but it does
not change this reading. With `present` true, `codec` and `rate_hz` are the
track ffmpeg negotiated; with it false they are empty and zero. A camera
that sends no audio track makes `camera_audio` do nothing.

`camera.config.credentials` says whether a camera login was read and where
it came from: `environment`, `file`, or `none`. **It carries no password
field at all** - not empty, not masked, not present. `user` is the login
name, which is not the secret. SPEC.md section 3 keeps the password in the
environment or in a mode 0600 file outside the repository, and nothing in
the web interface can read it, set it, or choose which file the server
reads.

`tailscale` is what Tailscale says about this box. It is read at most once
a minute and cached, so reloading the screen does not shell out again.
**Every field is best effort, and none of it is a failure.** A box with no
Tailscale answers `{"installed": false, ...}` with `err` saying so, and the
collector is unaffected either way: nothing in the measurement path depends
on the network (SPEC.md section 9.0.1).

The dashboard reports this. It never configures it. Setting up `tailscale
serve` is the owner's job (SPEC.md section 9.0), and the only commands this
program runs are `tailscale status --json` and `tailscale serve status
--json`, both read-only.

| Field | Meaning |
|---|---|
| `installed` | the `tailscale` command is on the box |
| `running` | `tailscaled` answered and its backend state is `Running` |
| `backend` | the backend state as Tailscale words it: `Running`, `Stopped`, `NeedsLogin` |
| `name` | the node's MagicDNS name, without the trailing dot |
| `serving` | a `tailscale serve` target points at `http_addr` |
| `serve_url` | what to open when `serving` is true, otherwise empty |
| `funnel` | the dashboard is published to the **public internet** |
| `key_expiry_ms` | when this node's key expires, or `0` when expiry is disabled |
| `err` | why the answer is incomplete, in plain words, or empty |
| `http_addr` | the dashboard's listen address, which a serve target has to point at |

`serving` matches a serve target against `http_addr` on the port, and treats
every loopback name as one name: `http://127.0.0.1:8080`,
`http://localhost:8080` and `127.0.0.1:8080` all serve an `http_addr` of
`127.0.0.1:8080`, `localhost:8080` or `:8080`. `:8080` listens on every
interface, loopback among them, so a loopback target reaches it; it is still
not a loopback address, and the collector warns about it at start. A
`http_addr` that names a routable address has to match exactly.

`funnel` true is a **fault, not a reading**. Funnel publishes the node to the
public internet, and `auth_mode: tailscale` trusts the
`Tailscale-User-Login` header, which is only safe because `tailscale serve`
is the sole path to a loopback listener. With Funnel on that trust is
misplaced. The collector logs it at ERROR, sends a failure ping through the
dead-man's switch, and writes a `tailscale_funnel` row in the health log.

`key_expiry_ms` being non-zero is worth acting on. A node whose key expires
drops off the tailnet, and the dashboard then looks exactly like a dead box
(SPEC.md section 9.0).

**No field here carries key material.** `tailscale status --json` holds the
node key, every peer's key and the tailnet's user list; the server decodes
the few fields above out of it and drops the rest. The raw answer never
reaches a response or the log.

### `POST /api/camera/probe`

Tries each RTSP path on the camera and reports what works. It is the
dashboard's form of `stompwatch probe-camera` and runs the same code.

```json
{"tried": [{"path": "Preview_01_sub", "ok": true,
            "detail": "640x360 at 15 fps (h264)"},
           {"path": "h264Preview_01_sub", "ok": false,
            "detail": "401 Unauthorized"}],
 "sub_path": "Preview_01_sub", "main_path": "Preview_01_main",
 "audio": {"known": true, "present": true, "codec": "aac", "rate_hz": 16000},
 "clock_drift_ms": 420, "clock_readable": true}
```

It is a POST, not a GET, because it makes the box connect out to the
camera. It takes no body and **changes no setting**: it reports what works,
and the owner applies it through `PUT /api/settings`.

`tried` lists every stream attempted, the sub-stream paths first and then
the main stream, with the reason each failure failed. With
`camera_rtsp_path` empty the sub-stream paths are the seven known cameras,
in order, so `tried` can hold seven entries before the main stream; a path
may carry a query, as `cam/realmonitor?channel=1&subtype=1` does. Every `detail` goes
through `Stream.Redact`, so a password that ffmpeg repeated back in an
error never reaches the browser. `sub_path` and `main_path` are the paths
that answered, or empty when none did.

`audio` says whether the sub-stream that answered carries an audio track.
`known` is false when no path answered, and then nothing about audio was
measured. With `present` true, `codec` and `rate_hz` are the track ffmpeg
negotiated, such as `aac` at 16000 Hz. Many cameras send no audio track, and
then `camera_audio` does nothing (SPEC.md section 15 decision 22). The probe
itself never records audio: it keeps `-an` and throws its output away.

`clock_drift_ms` is the camera clock
minus the host clock; `clock_readable` is false when the camera does not
report its clock, and then `clock_drift_ms` is zero.

Failures:

- 400 when `camera_host` is empty, or when no camera login is loaded. The
  message says what to set. The probe always goes to `camera_host` and
  `camera_port` from the config file; the dashboard cannot change them.
- 409 when a probe is already running. One probe runs at a time.
- 504 when the probe runs past its deadline. The message says so plainly.

### `GET /api/logs?from=&to=&level=&q=&limit=&offset=`

Reads the rotating JSON log files under `log_dir`, newest first. `level` is
repeatable (`DEBUG`, `INFO`, `WARN`, `ERROR`, `RAW`). `q` is a case-insensitive
substring of the whole line. `limit` 1 to 1000, default 200. `offset` passes
over that many of the newest matching lines, so the reply is the next page
back. It counts matching lines, not lines read, and starts at 0.

```json
{"truncated": false, "more": true,
 "lines": [{"ts_ms": 0, "level": "WARN", "msg": "free space is low",
            "attrs": {"dir": "/data", "free_mb": 900}}]}
```

`more` is true when at least one older matching line exists past this page.
There is no total: counting every matching line means reading the whole log,
which is the cost the backwards scan exists to avoid, so the reply answers
only whether there is another page.

`truncated` is true when the scan stopped at its byte budget (16 MiB) before
reaching `from`. A line that is not valid JSON is returned with
`level: "RAW"` and the text in `msg`, never dropped. `attrs` is `null` when
the line carries nothing but a time, a level and a message.

### `GET /api/settings`

```json
{"settings": [{"key": "threshold_db", "value": "15", "value_num": 15,
               "default": "15", "default_num": 15,
               "source": "file", "type": "float", "min": 1, "max": 60,
               "label": "Trigger level above baseline", "unit": "dB",
               "live": true, "note": ""}]}
```

`value` is the text the config file would carry. `value_num` is the same
value counted in the unit `min` and `max` use, so a form can range-check it
without parsing Go duration text: `baseline_window` reads `"10m0s"` and
`600`. It is `null` for a clock time such as `quiet_start`, which is not one
quantity with a range.

`source` is `file` when the value comes from the config file and `db` when a
row in the `config` table overrides it. Only the keys listed in
`settings.Editable` appear.

`type` is one of `float`, `int`, `duration`, `time`, `text`, or `bool`.
`min` and `max` mean nothing for `text` and `bool`, and `value_num` and
`default_num` are `null` for both. A `text` value is still checked by
`config.Validate`, which is the only thing that decides what a key accepts.
A `bool` value is the text `true` or `false`, as the config file writes it.

`note` is one sentence the interface must show with the field, or empty when
there is nothing to say. It carries what a range cannot: `camera_audio` says
that the camera will record speech in clear and that the measuring
microphone's clips are unaffected. It is the server's words, so the warning
cannot be lost by an interface that only draws inputs.

### `PUT /api/settings`

Body: `{"threshold_db": "12", "quiet_start": "22:30"}`. The merged settings
are validated as a whole first, by `settings.Live.Apply`; only when that
returns does the handler write the rows with `store.PutSettings`. A value
that fails validation rejects the **whole request** with 400, changes
nothing, and never reaches the disk. Sending `null` for a key deletes its row
so the file default returns. Returns the same body as `GET`.

Each setting whose value changed is logged at INFO with the login, the old
value and the new value. One `settings_changed` row in `system_health` names
the login and every change, as `key: old -> new`. A key sent with the value
it already had is left out, and a request that changed nothing writes no
row. `system_health` rows are never edited, so this is the audit record.

### Mute windows

- `GET /api/mute-windows` -> `{"windows": [{"id": 1, "start_ms": 0,
  "end_ms": 0, "reason": "party"}]}`
- `POST /api/mute-windows` with `{"start_ms":0,"end_ms":0,"reason":""}` ->
  201 and the created window. `end_ms` must be after `start_ms`.
- `DELETE /api/mute-windows/{id}` -> 204.

Adding a window writes a `mute_window_added` row to `system_health`, and
removing one writes `mute_window_removed`. Each names the login, the window
number, its start and end, and its reason.

**A mute window never stops the collector measuring, detecting, or recording
a clip.** Nothing about capture or storage changes, and no stored row is
altered or deleted: raw data is immutable (SPEC.md section 3 rule 3), and a detected
event is a measurement that happened. A window only marks the events the
owner can explain, at read time. What it changes is what the reading says:
`muted` on the event object, the `muted` filter on `GET /api/events`, the
counts in `GET /api/summary`, and the rows in the CSV export. Adding or
removing a window takes effect on the next request; nothing is cached.

### `GET /api/export/events.csv?from=&to=&status=verified`

`status` defaults to `verified` and is repeatable, but **only the three
reviewed decisions are allowed**: `none` returns 400. The claim this file
makes is that a person looked at every event in it. The file is UTF-8 with a
header row and `Content-Disposition: attachment`. Columns:

```
id,started_local,ended_local,duration_s,laeq_db,lamax_db,baseline_db,
class,confidence,level_uncertainty_db,sensitivity_source,
review_status,review_note,reviewer,reviewed_local,audio_sha256
```

`level_uncertainty_db` is the plus or minus on that row's three levels, and
`sensitivity_source` is `datasheet` or `measured`. Both come from **that
event's own** `capture_settings` row, not from what is in force now, so one
file may carry rows measured under different calibrations. Both are empty for
an event recorded before the history began: a CSV has no comment syntax, so a
column is the only honest way to carry this, and an empty cell says the
settings were not recorded rather than claiming a figure.

A muted event is never written to the file, whatever its review status. The
export is evidence, and a complaint should not rest on an evening the owner
already knows about.

Local times are RFC 3339 with the offset, because this file is read by a
person, not by a program. `duration_s` and the three levels carry one decimal
place and `confidence` carries two. `from` and `to` are both required: the
file name states the range.

A field that starts with `=`, `+`, `-` or `@` is written with a single quote
in front of it, because a spreadsheet reads such a field as a formula. The
review note is written by a person and is the field that matters, but the rule
is the same for every column so there is nothing to remember.

`Content-Disposition: attachment; filename="events-<from>-to-<to>.csv"`, with
the dates in `2006-01-02` form and in the server's local time zone.

### `GET /api/export/timeline.png?from=&to=&status=verified&width=&height=`

A rendered PNG of the timeline for the range, with event markers for the
matching reviews and quiet hours shaded. `width` 480 to 2400, default 1200;
`height` 240 to 1200, default 400. Rendered in Go with `image/png` and an
embedded bitmap font, so there is no runtime font dependency.

`status` defaults to `verified` and is repeatable, the same as the CSV. The
points use the same bucketing as `GET /api/timeline`, and a range longer than
31 days is refused the same way.

The picture is drawn on a light background rather than in the dashboard's dark
theme, because it is printed or pasted into an email. It carries the LAeq
trace, the quiet hours shaded behind it, a tick on the time axis for each
matching event, at least three labeled levels in dB, labeled times, and a
caption naming the range, the number of events, and the plus or minus on
every level in the picture. The uncertainty in the caption is the one in
force now, because the picture is of a range and not of one event; a range
that crosses a change of calibration says so. Two points that are not
neighboring buckets are left unjoined: the gap is where the measurement
stopped. A range with no measurement in it says so in words rather than
drawing a flat line at zero. The response carries
`Content-Disposition: inline; filename="timeline-<from>-to-<to>.png"`, so the
owner can save it with a name that says what it is.

## Configuration keys for the calibration

| Key | Default | Meaning |
|---|---|---|
| `sensitivity_dbfs` | `-13.0` | The dBFS level a 94 dB SPL tone at 1 kHz produces. |
| `sensitivity_source` | `datasheet` | `datasheet` or `measured`. |
| `sensitivity_uncertainty_db` | `2.0` | The plus or minus, in dB, from 0 to 10. |
| `sensitivity_measured_on` | empty | `YYYY-MM-DD`, the day it was measured. |
| `sensitivity_reference` | empty | Free text: what it was measured against. |

`measured` requires `sensitivity_measured_on`, and the date must parse.
`datasheet` requires `sensitivity_measured_on` and `sensitivity_reference` to
be empty, so the two states cannot blur.

**None of the five is editable from the dashboard**, and none ever will be.
They do not tune the instrument; they describe how it was calibrated, and
every level it has ever reported rests on them. A web form that could set
`sensitivity_source` to `measured`, with a date and a reference of its own
choosing, could claim a calibration that never happened.

`stompwatch calibrate` prints all four of the last four keys, stamped with the
day it ran, so that pasting what it prints leaves the file consistent.

## Configuration keys added in Phase 3

| Key | Default | Meaning |
|---|---|---|
| `http_addr` | `127.0.0.1:8080` | Listen address. Empty disables the server. |
| `auth_mode` | `tailscale` | `tailscale`, `trusted_header`, or `none`. |
| `auth_header` | `Tailscale-User-Login` | Header that carries the login. |
| `auth_name_header` | `Tailscale-User-Name` | Header that carries the display name. |
| `quiet_start` | `22:00` | Start of quiet hours, local time, `HH:MM`. |
| `quiet_end` | `07:00` | End of quiet hours. A wrap past midnight is normal. |
| `recording_pause` | *(empty)* | One or more daily spans with no events, no audio clips and no camera video, local time, `HH:MM-HH:MM`, separated by commas. The windows must not overlap or touch. Empty means no pause. The level of every second is still stored. Each start and end is a `recording_pause` health record. |
| `log_file_mb` | `20` | Size at which the log file rotates. |
| `log_files` | `5` | Files kept, including the current one. |
| `retention_days` | `90` | Days the audio and video files of an event are kept. After that retention deletes the files and records each one in `media_purge` as purged by `retention`; the event and every row stay. `0` keeps every recording forever. |

`http_addr` that is not loopback logs a prominent warning. An address with
no host, such as `:8080`, is not loopback: it listens on every interface.
With `auth_mode = none` it is a refusal to start, not a warning.

## Configuration keys added in Phase 2

| Key | Default | Meaning |
|---|---|---|
| `camera_host` | empty | Camera host name or address. Empty switches video off. |
| `camera_port` | `554` | RTSP port. |
| `camera_rtsp_path` | empty | Sub-stream path. Empty tries `Preview_01_sub`, then `h264Preview_01_sub`, on every reconnect in turn. |
| `camera_rtsp_path_main` | empty | Main-stream path. Empty puts `_main` where the sub path has `_sub`. |
| `camera_credentials_file` | empty | A mode 0600 file with `CAMERA_USER=` and `CAMERA_PASS=` lines. The environment variables of the same names win when set. |
| `video_dir` | `/data/clips/video` | Event clips under `YYYY/MM/DD`, and the segment ring under `ring`. |
| `video_ring_minutes` | `10` | How much of the sub-stream the ring keeps. |
| `video_segment_seconds` | `10` | Segment length. Clip edges land on segment edges. |
| `video_main_on_event` | `false` | Not built, and does nothing. It is parsed so that an old config file loads, and `true` logs a WARN at start. |
| `camera_audio` | `false` | Record the camera's own audio track into the ring and the event clips, unfiltered. It holds intelligible speech. Applies to video recorded after a restart. |
| `ntp_server` | `pool.ntp.org` | The NTP server the clock check asks, for the host and the camera alike. |

The camera password is never in the config file, the database, a log line,
an error, or a response. It is not in the environment of any child process
either: the collector reads `CAMERA_USER` and `CAMERA_PASS` once at start,
then removes both from its own environment, and the environment it gives
ffmpeg drops every `CAMERA_` entry.

`camera_rtsp_path`, `camera_rtsp_path_main`, `video_ring_minutes`,
`video_segment_seconds`, and `camera_audio` are editable from the
dashboard, and all five need a restart. `camera_credentials_file` and
`video_dir` are not editable: a web form must not be able to point the
server at any file it likes to read, or at any directory it likes to write.
`camera_host` and `camera_port` are not editable either: the camera login
is sent to them, and a web form must not be able to send the password to
any host it likes (SPEC.md section 15 decision 26). A row for either left in
the `config` table by an earlier version is logged, recorded in
`system_health`, deleted, and not used. `video_main_on_event` is not
editable because it is not built.

`camera_audio` is editable, unlike `clip_lowpass_hz`: it is neither a
credential nor a path. `GET /api/settings` sends it with `"type": "bool"`
and a `note` that says what switching it on means, so no interface can show
it as a bare checkbox among the numbers.

## Settings that the running process reloads

`threshold_db`, `min_duration_ms`, `hangover_ms`, `cooldown_ms`, `max_event_s`,
`min_baseline_s`, `pre_roll_s`, `post_roll_s`, `baseline_window`,
`baseline_percentile`, `quiet_start`, `quiet_end`, `recording_pause`.

`retention_days` is live as well: the retention job reads it at each run,
so a change applies at the next run rather than within 100 ms. The
dashboard accepts 7 to 3650; 0, which keeps every recording forever, is a
config file decision.

Everything else needs a restart, and `GET /api/settings` says so with
`"live": false`. Capture, calibration, and path settings are never editable
from the web interface: changing them from a phone at 2am would invalidate
the measurements with no record of why.
