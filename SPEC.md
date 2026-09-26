# Impact Noise Monitor - Build Spec

## 1. What this is

A system that documents impact noise (footfall, running, jumping, stomping)
entering an apartment from the unit above. It continuously measures A-weighted
sound level, detects impact events, records short audio and video clips around
each one, and presents everything in a web dashboard where the owner reviews and
confirms events by hand.

The output needs to be credible enough to hand to a landlord, mediator, or code
enforcement officer. **Measurement correctness, timestamp integrity, and
auditability matter more than features.** A system that silently produces wrong
numbers is worse than no system.

## 2. Hardware and environment

stompwatch runs on any Debian-based Linux computer with systemd, on x86-64 or
arm64. What it needs:

- A USB measurement microphone that offers **S24_3LE, 2 channels, 48 kHz** on a
  raw ALSA `hw:` device (section 3).
- A local disk for the data. **Never NFS or any network filesystem**, which
  breaks SQLite locking.
- ffmpeg, only if a camera is used.

The table below is the reference setup it was built and tested on. The
measurements and the defaults in this document come from it.

| Component | Tested on | What else works |
|---|---|---|
| Host | ZimaBoard 832 - Intel Celeron N3450 (x86-64, 4 core), 8GB LPDDR4, 32GB eMMC, dual GbE, 2x USB 3.0, dual SATA, fanless | Any machine with 2 cores and 4 GB of memory, x86-64 or arm64. |
| OS | **Debian 13 (trixie)**, headless | Any Debian-based OS with systemd. |
| Storage | WD Red 1 TB 2.5-inch hard disk on SATA, mounted at `/data` (local ext4). The eMMC holds the OS only. | Any local storage. **Never NFS or any network filesystem**, which breaks SQLite locking. |
| Microphone | Eversolo EM-01 USB-C measurement mic (USB ID `262a:1a1b`, Savitech controller, ALSA card ID `EM01`). Offers S16_LE or S24_3LE, stereo, 48 kHz only. UAC1.0 class-compliant, 24-bit/48kHz onboard ADC, omnidirectional 6mm electret, sensitivity -13 dBFS +/-2 dB @1kHz, 20Hz-20kHz +/-1dB with per-unit calibration file, 130 dB SPL max, self-noise -84 dBFS @ +30dB internal gain. | Any USB measurement mic that offers **S24_3LE, stereo, 48 kHz** on a raw `hw:` device, ideally with a calibration file. |
| Camera | Reolink E1 Pro and Amcrest 5MP, over RTSP | Any RTSP camera. **Treat the model as configurable** - the RTSP URL, credentials, and stream paths all come from config, and nothing in the code may assume a specific model. Use a **fixed preset** (auto-tracking off). The camera's own microSD recording makes an independent backup. |
| Network | Wired GbE; EDUP MT7921AU USB adapter (`mt7921u`, mainline) as the wireless fallback | Any network. Wired is preferred. |
| Sensors | Audio only | No accelerometer, no vibration sensor, no GPIO. |

Deployment: `systemd` unit running a single static binary. Containers optional
and not required - one binary plus one SQLite file is the whole system.

## 3. Non-negotiable constraints

Read these before designing anything. They are requirements, not preferences.

1. **Capture from the raw ALSA hardware device (`hw:N,0`), never `default`.**
   PipeWire and PulseAudio can silently insert echo cancellation, noise
   suppression, or resampling. Any of those invalidates every measurement.
   The device string is configurable but must default to a `hw:` form
   (`hw:EM01,0`), and the app must refuse to start if configured with
   `default` or a `plug:` device.

2. **Audio clips are low-pass filtered at 500 Hz and decimated to 2 kHz before
   ever touching disk.** Do the filtering in the capture path so an unfiltered
   clip cannot exist on disk, even transiently, even on crash.
   *Rationale:* Several US states and many other countries require the consent of everyone
   recorded ("all-party consent").
   Impact noise is not an "oral communication" and is lawful to record, but
   incidental speech capture is a real legal problem. A 500 Hz brickwall
   destroys speech intelligibility; decimating to 2 kHz makes recovery
   physically impossible rather than merely difficult. **Do not make this
   configurable, do not add an "unfiltered" mode, do not add a bypass flag.**

3. **Raw data is immutable.** Review decisions live in a separate table and
   never modify sample rows, event rows, or media files. Retention is the only
   thing that deletes data, and every deletion is logged.

4. **Failures must be loud.** A silent failure that loses a night of data is
   the worst possible bug. Every dropout, reconnect, clock drift, and write
   error is counted, persisted, and surfaced in the UI.

5. **Never block the audio reader.** The capture goroutine must never be
   stalled by DSP, disk, or database work. Use bounded channels and drop with
   a counter rather than applying backpressure to the reader.

6. **The camera password is on ffmpeg's command line.** ffmpeg takes the
   RTSP URL, password included, as an argument. ffmpeg 7.1 cannot read the
   input URL from a file: `-/i` was tried on the box and refused with
   "Unrecognized option '/i'". Linux shows every process's command line to
   every local user (`ps`, `/proc/PID/cmdline`, `systemctl status`). The
   mitigation is on the box, not in the code: mount `/proc` with
   `hidepid=invisible` (the fstab line
   `proc /proc proc defaults,hidepid=invisible 0 0`, then
   `mount -o remount /proc`), which hides other users' processes from users
   who are not root. `install.sh` warns when a `camera.env` exists and
   `/proc` has no `hidepid`. It never changes the mount itself.

## 4. Language, dependencies, layout

Go 1.22+. Single module, single binary.

**Dependencies:** `modernc.org/sqlite` (pure Go, no cgo - keeps the build
static and simple). Standard library for everything else: `net/http`,
`database/sql`, `log/slog`, `os/exec`, `encoding/binary`, `context`.
**Justify any additional dependency before adding it.** Do not pull in a web
framework, an ORM, a DSP library, or a logging library.

```
cmd/stompwatch/          main, flags, subcommands: run | calibrate | verify-dsp
internal/config/       file + DB-backed config, live reload
internal/audio/        ALSA capture, arecord supervision, frame types
internal/dsp/          biquads, A-weighting, filters, envelope, autocorrelation
internal/meter/        LAeq/LAmax binning, rolling baseline
internal/detect/       event state machine, classification
internal/clip/         audio ring buffer, WAV writer
internal/video/        RTSP segment manager, event extraction
internal/store/        SQLite schema, migrations, queries
internal/api/          HTTP handlers, SSE, range serving
internal/health/       counters, health endpoint
web/                   SPA assets, served via embed.FS
```

## 5. Concurrency model

Make this explicit in the code and document it in a package comment.

```
arecord (subprocess)
   | stdout, raw S24_3LE stereo
   v
captureLoop --> frameCh (bounded, drop+count on full)
                   |
                   +--> ringBuffer         (mutex-guarded, 30s of raw samples)
                   |
                   \--> dspLoop --> binCh --> meterLoop --> detectLoop
                                                               |
                                                               +--> clipWriter
                                                               +--> videoExtractor
                                                               \--> store (batched tx)
```

- Every loop takes a `context.Context` and exits cleanly on cancel.
- `errgroup`-style supervision: if any loop dies, log loudly, restart it with
  backoff, and record the outage in `system_health`.
- Shutdown flushes pending bins and closes the DB cleanly.
- `-race` must pass. Include a test that runs the full pipeline with synthetic
  input under the race detector.

## 6. Phase 1 - Measurement core

Build this first. It is useful on its own. **Write the tests before the DSP.**

### 6.1 Capture

**Verified device capability** (`/proc/asound/card0/stream0` on the actual
hardware - this is ground truth, not a guess):

```
EVERSOLO EM-01, full speed, USB Audio, Interface 2
  Altset 1: S16_LE,   2ch, 48000 Hz
  Altset 2: S24_3LE,  2ch, 48000 Hz
```

**There is no `S32_LE` altset and no mono altset.** Capture
`-f S24_3LE -r 48000 -c 2` and take a single channel. Do not request mono or
32-bit - on a `hw:` device there is no plug layer to convert, so the open
simply fails.

- Spawn `arecord -D hw:EM01,0 -f S24_3LE -r 48000 -c 2 -t raw` and read stdout.
  Piping is preferred over cgo ALSA bindings. The device string is
  configurable; **`hw:EM01,0` addresses the card by its stable ALSA ID**, so
  no udev rule and no dependence on card numbering.
- Supervise: restart on exit with exponential backoff, count restarts, record
  every gap with its duration in `system_health`.
- **S24_3LE decoding:** three bytes little-endian per sample, sign-extended
  from bit 23 - not a 4-byte int32. Frames are interleaved L,R.

  ```go
  v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
  if v&0x800000 != 0 {
      v |= ^0xFFFFFF // sign-extend
  }
  f := float64(v) / 8388608.0 // 2^23
  ```

  Unit-test this against known byte patterns including the sign boundary
  (`0x7FFFFF`, `0x800000`, `0xFFFFFF`). A sign-extension bug produces
  plausible-looking audio with a wrong level, which is the worst kind of bug
  in this system.
- **Channel selection is configurable** (`capture_channel`, default 0 = left)
  because this mic presents stereo for a single capsule. Determine empirically
  whether both channels carry signal or only one; if they are duplicates,
  still take one rather than summing - summing correlated channels adds 6 dB
  and silently breaks calibration.
- 24-bit is the right choice here, not 16: at -13 dBFS sensitivity, full scale
  is ~107 dB SPL and the mic's own noise floor sits near 23 dB SPL. 16-bit
  would just cover it; 24-bit leaves margin and keeps quantization well below
  the acoustic floor for free.
- Detect a stuck stream (bit-identical frames, or all-zero for > 5 s) and treat
  it as a failure - a mic that has silently stopped looks like a very quiet
  night otherwise.

### 6.2 Gain guard

At startup, read the ALSA capture control via `amixer`/`amixer -c N sget`.
Log the value. **If it differs from `expected_capture_gain` in config, refuse
to start** with a clear error. A gain change silently invalidates every
measurement on both sides of it. Re-check periodically and alert on drift.

### 6.3 Calibration

```
dB_SPL = dBFS - sensitivity_dbfs + 94.0
```

with `sensitivity_dbfs` configurable, default `-13.0`.

Parse a REW-format calibration file: plain text, whitespace or comma separated,
columns `freq_hz  magnitude_db  [phase_deg]`, with `*` and `#` comment lines.
For Phase 1, apply the mean magnitude across 800-1250 Hz as a broadband offset.
**Structure the code as a `Calibration` interface** so a per-band FIR
correction can replace the scalar later without touching callers.

### 6.4 DSP

Implement biquads as **Direct Form II transposed**, `float64` state, in
`internal/dsp`. One `Biquad` type, one `Cascade` type. No allocations in the
per-sample hot path.

**A-weighting (IEC 61672).** Build from the standard analog poles -
20.598997, 107.65265, 737.86223, 12194.217 Hz - via bilinear transform at the
sample rate, as three cascaded sections, with overall gain normalized to 0 dB
at 1 kHz. Derive the coefficients in code at init; do not paste magic numbers.

**LAeq per 1-second bin:** `10*log10(mean(x_A^2))` plus the calibration offset.

**LAmax with Fast time weighting:** apply a 125 ms exponential moving average
to `x_A^2` (this is what "Fast" means in IEC 61672 - do not skip it and take a
raw sample peak), then take the maximum over the bin.

**Band energies per bin:** unweighted RMS in 20-120 Hz (low) and above 500 Hz
(high). Store both. Their ratio is the proxy for structure-borne impact noise
versus airborne noise from my own room, and it matters for arguing the source.

**Clip low-pass:** 4th-order Butterworth at 500 Hz = two cascaded biquads with
Q = 0.54120 and Q = 1.30656. Then decimate 48 kHz -> 2 kHz.

### 6.5 Rolling baseline

Maintain a low percentile (configurable, default 10th) of LAeq over a trailing
window (default 10 minutes), updated each bin. **Do not use a fixed threshold**
- ambient level changes through the day and a fixed value produces either
constant false positives at noon or total blindness at 3am.

Use a ring buffer plus a periodic sort, or a two-heap structure. At 600
samples, a naive re-sort per bin is fine; don't over-engineer it.

### 6.6 Event detection

State machine with these configurable parameters:

| Param | Default | Meaning |
|---|---|---|
| `threshold_db` | 15.0 | LAmax above baseline to trigger |
| `min_duration_ms` | 400 | must stay above before an event opens |
| `hangover_ms` | 2000 | below-threshold time before it closes |
| `cooldown_ms` | 5000 | after close, before a new event may open |
| `max_event_s` | 300 | force-close runaway events |

Persist per event: start/end timestamps, duration, LAeq, LAmax, baseline at
trigger, low-band energy, high-band energy, low/high ratio.

### 6.7 Classification

Band-pass 20-120 Hz -> full-wave rectify -> low-pass ~10 Hz -> decimate to 100 Hz
to get the envelope. Then:

- **running** - autocorrelation of the envelope shows a stable peak at a lag
  corresponding to 2-3 Hz (lag 0.33-0.50 s), sustained >= 3 s
- **jumping** - isolated peaks >= some multiple of the event median, with gaps
  >= 1 s between them
- **stomping** - impulses present, no stable cadence
- **unknown** - everything else. **Use this freely.** A wrong label is worse
  than no label; I will review every event by hand anyway.

Before those rules, three gates run, measured on 32 owner-labeled events
(section 15 decision 25):

- **airborne** - the high band is at least as loud as the low band (ratio
  <= 0 dB): a television, a voice.
- **steady** - the peak is under 6 dB over the median level of the 30 s
  before the event; or no second rose 5 dB over the one before it and the
  peak is under 5 dB over the event's own mean: a fan starting, or a level
  that was already there.
- **impact** - one hit: the gates pass, no cadence rule matches, and the
  event is 10 s or shorter.

The two rise values are stored with the event as `jump_db` and `rise_db`,
NULL when less than 10 s of levels came before it. The gates label; they
never stop an event from being stored.

Store the class, a confidence score, **and the downsampled envelope itself**
(as a BLOB) so classifications can be recomputed later without re-collecting
a month of nights.

### 6.8 Audio clips

- 30-second ring buffer of raw samples for pre-roll.
- On event close, write `pre_roll_s` (default 10) + event + `post_roll_s`
  (default 5).
- Pipeline: 500 Hz low-pass -> decimate to 2 kHz -> 16-bit PCM mono WAV.
- Path: `/data/clips/audio/YYYY/MM/DD/<event_id>.wav`
- Store SHA-256 of each file in `event_media`.

### 6.8.1 Dead-man's switch (heartbeat)

**Build this in Phase 1, not later.** It is the highest-value monitoring in the
system and a few dozen lines of code.

A goroutine pings a configurable heartbeat URL (healthchecks.io or equivalent)
every `heartbeat_interval` (default 5 min) **only while collection is actually
healthy** - audio frames arriving, last bin written within the last 2x
interval. If collection has stalled, **stop pinging** rather than reporting
success.

This is the bootstrap problem in a nutshell: every other monitor in this system
reports *through* infrastructure that may itself be down. A dead-man's switch
inverts it - silence is the alert. One check covers power loss, disk full,
kernel panic, network death, and a crashed collector at once, and it reaches me
even when nothing on my side is reachable.

- Send a separate `/fail` ping on a detected fatal condition so I get an
  immediate alert rather than waiting for the grace period.
- Failures to reach the heartbeat endpoint are logged and counted, never fatal.
- Configurable and skippable - an empty URL disables it.

Config keys: `heartbeat_url`, `heartbeat_interval`, `heartbeat_stall_factor`.

### 6.9 Persistence

- Batch inserts in a transaction, one commit per 10-30 s of bins.
- Retry `SQLITE_BUSY`; treat persistent write failure as a loud alerting
  condition, never swallow it.
- Check free disk space at startup and hourly; refuse to start below a
  configurable floor and warn in the UI well before that.

### 6.9.1 SQLite access pattern - get this right

The sustained load is ~1 row/s written and one interactive reader. That is far
below SQLite's limits. The failure modes here are not throughput; they are the
two standard ways Go programs misuse SQLite. Both are mandatory:

**Two separate `*sql.DB` handles, not one.**

```go
// writer: exactly one connection, ever
writeDB.SetMaxOpenConns(1)
writeDB.SetMaxIdleConns(1)
writeDB.SetConnMaxLifetime(0)

// reader: opened with mode=ro, pooled
readDB.SetMaxOpenConns(4)
```

`database/sql` pools connections and hands them out arbitrarily. With one pooled
handle, concurrent writes land on different connections and produce
`SQLITE_BUSY` storms and `database is locked` errors that look like corruption
bugs but are pure misconfiguration. Funnelling all writes through a single
connection eliminates writer contention by construction. WAL lets the read pool
work concurrently with the writer.

**Begin write transactions as `IMMEDIATE`** (`_txlock=immediate` in the DSN).
A deferred transaction that starts reading and later tries to write can fail
with `SQLITE_BUSY_SNAPSHOT`, which is not retryable mid-transaction and forces a
full rollback.

**Connection pragmas** (writer DSN):

```
_pragma=journal_mode(WAL)
_pragma=synchronous(NORMAL)
_pragma=busy_timeout(5000)
_pragma=foreign_keys(ON)
_pragma=temp_store(MEMORY)
_pragma=cache_size(-65536)    // 64 MiB page cache; the box has 8 GB
_pragma=mmap_size(268435456)  // 256 MiB, helps range reads
```

`synchronous=NORMAL` under WAL cannot corrupt the database, but a power loss can
lose the most recent commits - bounded here by the batch interval, so at most
~30 s of 1-second bins. That is an acceptable trade for this workload; **record
it in the README** so the limitation is documented rather than discovered.

**Keep read transactions short.** A long-lived reader prevents WAL checkpointing
and the `-wal` file grows without bound. In particular, **never hold an open
transaction while streaming SSE** - query, close, then stream. Monitor `-wal`
file size and log a warning above 64 MiB; run
`PRAGMA wal_checkpoint(TRUNCATE)` during idle periods and after the nightly
integrity job.

**Reuse prepared statements** for the per-bin insert rather than re-preparing
each time.

**Retention deletes in chunks.** If pruning is ever enabled, delete in batches
of ~10k rows per transaction rather than one large `DELETE`, which takes a long
write lock and bloats the free list. At roughly 1.2 GB/year, prefer never
deleting `samples_1s` at all - clips are what consume real space.

## 7. Phase 2 - Video

Stop and check in with me before starting this.

**The camera is a dumb stream source. Do not use its own motion detection, its
SD recording, or any CGI "start recording" call to trigger anything.** The
audio detector decides what an event is. The camera-side path cannot produce
pre-roll: by the time a camera knows something happened, the moment is gone.

- Supervise `ffmpeg` writing the RTSP substream continuously to 10-second
  segments in a ring directory (`-f segment -segment_time 10 -c copy`, no
  re-encode, `-rtsp_transport tcp`). The ring is sized from
  `video_ring_minutes` (default 10) and pruned oldest-first.
- On event, select the segments spanning pre-roll -> post-roll and concat them
  into one clip. Because segments are copied, not re-encoded, clip boundaries
  land on segment edges - pad outward rather than re-encoding to trim.
- **Stream URLs come from config, with these defaults to try in order:**
  `rtsp://USER:PASS@HOST:554/Preview_01_sub` (current firmware) and
  `rtsp://USER:PASS@HOST:554/h264Preview_01_sub` (older firmware).
  Main stream substitutes `_main` for `_sub`. Provide a
  `stompwatch probe-camera` subcommand that tries each form, reports which
  works, and prints the negotiated resolution and frame rate.
- Optionally pull a main-stream clip for the event window only. This is not
  built. The config key `video_main_on_event` is still parsed, so an old
  config file loads, but it does nothing, and `true` logs one WARN at start
  that says so.
- Auto-reconnect with backoff. **Log every disconnect and its duration**, and
  surface total camera uptime in the UI - a gap in coverage is something I need
  to know about honestly, not something to paper over.
- Verify camera and host clocks against the same NTP source; warn above 2 s
  drift. Timestamp integrity is the foundation of the entire record.
- Never ingest camera audio. Force `-an` on every ffmpeg invocation.

## 8. Schema (SQLite)

```sql
-- epoch seconds as INTEGER PRIMARY KEY => rowid => table is physically
-- clustered in time order; range scans are optimal with no secondary index.
CREATE TABLE samples_1s (
  ts          INTEGER PRIMARY KEY,
  laeq        REAL NOT NULL,
  lamax       REAL NOT NULL,
  low_energy  REAL NOT NULL,
  high_energy REAL NOT NULL,
  baseline    REAL NOT NULL
);

CREATE TABLE samples_1m (   -- incrementally maintained rollup
  ts INTEGER PRIMARY KEY,   -- minute boundary
  laeq_min REAL, laeq_mean REAL, laeq_max REAL,
  lamax_max REAL, n INTEGER
);

CREATE TABLE events (
  id INTEGER PRIMARY KEY,
  started_ts INTEGER NOT NULL, ended_ts INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  laeq REAL, lamax REAL, baseline_at_trigger REAL,
  low_energy REAL, high_energy REAL, low_high_ratio REAL,
  class TEXT, confidence REAL, envelope BLOB,
  created_ts INTEGER NOT NULL
);
CREATE INDEX idx_events_started ON events(started_ts);

CREATE TABLE event_media (
  event_id INTEGER NOT NULL REFERENCES events(id),
  kind TEXT NOT NULL,          -- 'audio' | 'video'
  path TEXT NOT NULL, bytes INTEGER, duration_ms INTEGER,
  sha256 TEXT NOT NULL,
  PRIMARY KEY (event_id, kind)
);

-- separate table: review NEVER mutates events or samples
CREATE TABLE event_review (
  event_id INTEGER PRIMARY KEY REFERENCES events(id),
  status TEXT NOT NULL,        -- 'verified' | 'rejected' | 'unsure'
  note TEXT,
  reviewed_ts INTEGER NOT NULL
);

CREATE TABLE mute_windows (
  id INTEGER PRIMARY KEY,
  start_ts INTEGER NOT NULL, end_ts INTEGER NOT NULL,
  reason TEXT NOT NULL
);

CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_ts INTEGER);

CREATE TABLE system_health (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,          -- capture_gap | camera_disconnect | clock_drift
                               -- | gain_change | write_error | disk_low
  detail TEXT, duration_ms INTEGER
);
```

Migrations: numbered `.sql` files embedded via `embed.FS`, applied in order,
tracked in a `schema_version` table. No migration framework.

**Nightly integrity job:** `PRAGMA integrity_check`, then `VACUUM INTO` a
dated snapshot (not a filesystem copy of a live DB), SHA-256 it, log the hash.
The whole record being one verifiable file is an evidentiary advantage - a
single artifact with a hash proving it wasn't altered after the fact.

## 9. Phase 3 - Web dashboard

Stop and check in with me before starting this.

Single Go binary with the built SPA embedded via `embed.FS`.
**No login system of our own** - see section 9.0 for how access is actually gated.

### 9.0 Access and authentication

Two paths reach the dashboard. **There is no login system of our own** and no
password anywhere in this application.

- **On the LAN:** a reverse proxy that authenticates users (for example
  with OIDC) and passes the login in a header.
- **Remotely: Tailscale.** The box joins my tailnet; the dashboard is reachable
  from my devices over WireGuard. No inbound ports, no port forwarding, no
  reverse proxy in the middle, no third party in the traffic path.

**Bind the HTTP listener to `127.0.0.1` only - never `0.0.0.0`.** Make the bind
address configurable but default to loopback, and log a prominent warning at
startup if it is set to a routable interface. Every path in must terminate at
something that has already authenticated the caller.

#### Tailscale setup (README, not code's job)

- `tailscaled` on the host; MagicDNS provides a stable name.
- `tailscale serve --bg http://127.0.0.1:8080` - automatic HTTPS, reachable
  only from the tailnet.
- Tailnet ACLs restrict which devices may reach the port at all.
- Tag the node (e.g. `tag:stompwatch`) and **disable key expiry** on it, or use a
  non-expiring auth key. A node whose key silently expires drops off the
  tailnet and the dashboard becomes unreachable with no obvious cause - the
  single most common Tailscale operational surprise, and it would look exactly
  like the box being dead.

#### Auth modes

Authentication is the network layer's job. The app reads identity for display
and audit only.

1. `auth_mode: tailscale` (default) - trust `Tailscale-User-Login` /
   `Tailscale-User-Name`, injected by `tailscale serve`. **Safe only because
   the listener is loopback-bound and `tailscale serve` is the sole path to
   it** - state this explicitly in the README. Record the identity on every
   `event_review` row so the audit trail shows who marked what.
2. `auth_mode: trusted_header` - configurable header name, for an
   authenticating reverse proxy.

   In both modes an `/api` request with no login header is refused with 401
   (section 15 decision 27). It was once served as `unknown`.
3. `auth_mode: none` - dev only. **Refuse to start** in this mode if the bind
   address is not loopback.

#### Why not a public tunnel

Recorded because this was considered and rejected: a proxied tunnel buffers SSE
unless explicitly disabled, can behave differently on HTTP range requests so
clip seeking works locally but fails remotely, and routes video of my own home
through a third party's network. WireGuard is a direct path with none of those
properties. Tradeoff accepted: remote access requires the Tailscale client, so
the dashboard cannot be handed to anyone as a URL. That is fine - sharing
happens through the CSV and clip export, not by granting access.

#### Monitoring consequence

A public tunnel would have supplied vendor-side disconnect notifications.
Tailscale does not. **The dead-man's switch in section 6.8.1 is therefore the only
out-of-band alert, and is load-bearing rather than optional.** With it
disabled, an offline box is invisible until I happen to look.

### 9.0.1 Transport notes

Traffic arrives over WireGuard rather than through a proxy, so no
anti-buffering workarounds are required. Still set `Cache-Control: no-cache`
and flush after every write on the SSE stream, and serve media with
`http.ServeContent` so range requests work. **Verify clip seeking and the live
meter over Tailscale from a phone on cellular** - that is the real deployment
path, and the only one worth calling tested.

**Nothing in the collector may depend on network reachability.** If Tailscale,
the LAN, or the internet goes down, capture, detection, clip writing, and
SQLite commits continue unaffected; R2 uploads queue and drain on
reconnection. No health check, retry path, or shutdown condition in the
collector may reference network state.

### 9.1 Frontend stack

React + Vite + Tailwind + **shadcn/ui**, scaffolded from the `dashboard-01`
block as the starting layout:

```bash
npx shadcn@latest init
npx shadcn@latest add dashboard-01
```

Node is a **build-time dependency only**. `vite build` emits `web/dist`, which
is embedded with `//go:embed all:dist`. The shipped artifact remains one static
Go binary with no runtime Node, no CDN, and no external asset fetches.
Commit `web/dist` or build it in CI - deploying must not require a toolchain
on the ZimaBoard.

**How the block maps onto this app:**

| dashboard-01 piece | Use |
|---|---|
| `ChartAreaInteractive` | the dBA timeline, with range toggles (night / 24h / 7d / 30d) |
| `DataTable` (TanStack Table) | the event table - sorting, filtering, pagination |
| `SectionCards` | summary stats: events tonight, loudest event, camera uptime, upload queue depth |
| `AppSidebar` / `SiteHeader` | nav - **three items only**: Tonight, Events, System |

**Rules for adapting it:**

- **Delete the demo data path entirely.** The block ships with a `data.json`
  fixture and placeholder columns. Do not let the demo's shape influence the
  API or the schema - the schema in section 8 is authoritative. Replace the data
  wiring before building any feature on top of it.
- **Drop `@dnd-kit`.** The block uses it for drag-reorderable table rows,
  which is useless here and is dead weight.
- Keep `recharts`, `@tanstack/react-table`, `lucide-react`.
- shadcn components are copied into the repo, not installed as a dependency -
  so we own the code and it cannot break under us. Treat `components/ui/*` as
  ours to edit.
- **Dark mode must work and should be the default.** Most review happens at
  night; a white screen at 2am is genuinely unpleasant. The block includes
  theming - wire it up rather than leaving it at defaults.
- **The event detail view is custom work.** dashboard-01 has no media player.
  Build it: audio element with waveform, video element, and a synchronized dB
  trace sharing one timestamp axis.
- **Verify the data table on a narrow viewport.** TanStack tables default to
  something unusable on a phone; provide a card-style stacked layout below the
  `md` breakpoint. See the mobile requirement below - it is not optional.

### 9.2 Design direction

**This is a measurement instrument, not a SaaS product.** Its ancestors are the
sound level meter and the strip-chart recorder, not a marketing analytics
dashboard. One user, usually at night, often on a phone, doing one repetitive
job: deciding whether each detected event is real. Everything serves that.

**Three screens, not six.** Collapse the nav to **Tonight** (live meter +
today's trace + tonight's events), **Events** (the full searchable history and
the review workflow), and **System** (health, logs, settings, uploads). Every
additional screen is a place to get lost in at 2am.

**Color is reserved for data.** The chrome - sidebar, headers, table, controls
- is achromatic. The only saturated color anywhere is the level ramp on the
timeline (quiet -> loud) and the review status marks. This is what real
instrument interfaces do, and it means a loud night is legible at a glance
without reading a single number. Do not spend color on decoration, on gradient
washes, or on giving each stat card its own tint.

**Dark by default, and genuinely dark** - not a tinted near-black
(`#0B0B0B`, `#111`) standing in for black. Pick a real base and commit to it.

**The timeline is the one bold element.** Spend the design budget there: a
continuous trace with quiet hours shaded as ground truth, event markers as
structure, and a level ramp that reads instantly. Everything else stays quiet
and disciplined. If a decoration doesn't help me decide whether an event is
real, cut it.

**Numbers must not jitter.** Apply `font-variant-numeric: tabular-nums` to
every readout, table cell, and the live meter. A decibel value that shifts
horizontally as it updates looks broken and reads as amateur. One typeface
family for the whole interface; do not introduce a second face for display
text, and do not reach for monospace as a styling device for small labels.

**Avoid these - they are the block's defaults, not decisions:**

- Content chopped into identical rounded cards with the same soft gray shadow
  on each, regardless of hierarchy. The `SectionCards` row ships this way;
  reduce it to a quiet stat strip, or cut it from **Tonight** entirely if the
  timeline already conveys the same thing.
- All-caps tracked-out labels above headings, and eyebrow labels generally.
- Meta strings joined with middle dots (`A * B * C`).
- Arrows appended to button text.
- Entrance animations on load, fade-and-slide-up on sections, hover
  transitions on every card. **Motion only in response to my action** - a row
  settling when I mark it verified, a panel opening. Respect
  `prefers-reduced-motion`.

**Copy is part of the design.** Plain sentence case, active voice, no filler.
Name things as I'd say them: "Confirmed," not "Verification status: true."
The empty state matters more than usual here - **no events tonight is the good
outcome**, so it should read as reassurance, not as a failure or a blank
screen. Errors say what happened and what to do, in the interface's voice.

**Quality floor, unannounced:** responsive to a phone, visible keyboard focus,
reduced motion respected, sufficient contrast at night.

- **Timeline** - dBA over time with event markers, brushable from a week down
  to a single night. Serve wide ranges from `samples_1m`, never from raw.
- **Event table** - all event fields plus review status. Filter by date range,
  level, class, quiet hours, review status. Sortable.
- **Event detail** - audio clip with waveform, video clip, and a zoomed dB
  trace of the same window, sharing one timestamp axis.
- **Review workflow** - the most important screen. Confirm / reject / unsure
  plus a note, with keyboard shortcuts (`j`/`k` to move, `v`/`x`/`u` to mark)
  so I can triage a night in a couple of minutes. Every export filters on
  `verified`. The claim I want to make is not "my detector is perfect" but
  "every event in this report was reviewed by a human."
- **Live** - current level over SSE.
- **Export** - date range in; CSV of verified events plus a rendered timeline
  chart, suitable for attaching to an email or a complaint.
- **Settings** - thresholds, cooldown, retention, quiet hours, mute windows.
  Writes to `config`; the running process reloads without restart via an
  internal reload path. Config file remains the source of defaults.
- **System** - health (capture gaps, camera uptime, clock drift, gain changes,
  disk, upload queue), logs, and settings on one screen with sections, not
  three separate destinations.
- **Logs** - a readable view of recent application log output, filterable by
  level and time range, searchable by substring. Use `log/slog` with a JSON
  handler writing to both stderr (for journald) and a rotating file under
  `/data/logs/`; the UI reads the file. Cap total log size (e.g. 5 files x
  20 MB) and never let logs compete for space with clips. Rationale: this box
  is headless in a closet, and "SSH in and run journalctl" is the wrong answer
  when I am checking on it from my phone at 2am.

  Keep the health counters and the raw log separate - counters answer "is it
  working," the log answers "what exactly happened." Link from a
  `system_health` row to the log view filtered to that timestamp.

**Serve media with `http.ServeContent`**, which handles Range requests
correctly. Without ranges the browser downloads whole clips before playing and
review becomes unusable.

**Media serving rules:** stream clips from disk by event ID - never expose a
filesystem path as a URL parameter, and validate that the resolved path stays
within the configured media root. Verify the stored SHA-256 on download (not on
every stream) so I can demonstrate a clip is unaltered. Return 404 rather than
500 when a file referenced by `event_media` is missing, and record the
discrepancy in `system_health`: a media file that has vanished is exactly the
kind of silent problem that should surface loudly.

**Mobile matters.** I will do a meaningful share of review from a phone.
The timeline, the event list, and clip playback must work on a narrow viewport;
the keyboard shortcuts are a desktop convenience layered on top, not the only
way to review.

## 10. Phase 4 - Offsite replication to Cloudflare R2

Stop and check in with me before starting this. Phase 1 must be collecting
reliably first; offsite copies of a broken dataset are worthless.

**Purpose, in priority order:** (1) tamper-evidence - an offsite object with an
upload timestamp helps establish that a clip existed at a point in time and was
not assembled after the fact; (2) survival of device loss, theft, or disk
failure. Treat this as part of the evidentiary chain, not just a backup.

### What gets uploaded

- **Audio clips** - always. They are tiny (2 kHz, 16-bit, mono: ~60 KB for a
  30-second clip).
- **Nightly SQLite snapshot** - the `VACUUM INTO` artifact from section 8, with its
  SHA-256. This is the entire record in one verifiable file.
- **Video clips** - configurable via `r2_video_policy`: `all`, `verified_only`
  (upload once I mark an event verified), or `none`. Default `verified_only`.
  Two reasons: it cuts volume by an order of magnitude, and the video is of my
  own home interior - I want a deliberate choice about what leaves the house,
  not everything by default.

Never upload raw `samples_1s` rows individually; they ride along in the
snapshot.

### Implementation

R2 is S3-compatible. Use `aws-sdk-go-v2` with a custom endpoint resolver
pointed at `https://<account_id>.r2.cloudflarestorage.com`, region `auto`.
(`minio-go` is a lighter alternative if the AWS SDK's dependency weight is
objectionable - either is an acceptable exception to the dependency rule.)

- **`upload_queue` table**: `id, kind, local_path, sha256, bytes, remote_key,
  status, attempts, last_error, queued_ts, uploaded_ts, etag`.
  Enqueue on clip write and on snapshot creation.
- **A dedicated uploader goroutine** drains the queue. It must **never block
  or slow the capture path** - failure to upload is a warning, never a reason
  to stop recording.
- **Exponential backoff with jitter**, capped (e.g. 1s -> 30min). Retry
  indefinitely; a week-long internet outage should resolve itself on
  reconnection, not require intervention.
- **Multipart** for anything over 8 MB.
- **Key layout:** `noise/<YYYY>/<MM>/<DD>/<event_id>-<kind>.<ext>` and
  `snapshots/<YYYY-MM-DD>.sqlite`. Date-partitioned so a date range maps to a
  prefix listing.
- **Store our own SHA-256 as object metadata** (`x-amz-meta-sha256`) rather
  than relying on ETag comparison - multipart ETags are compound and not a
  content hash. Record the returned ETag in `upload_queue` anyway.
- Surface upload lag, queue depth, and last error on the **Health** page.
  A queue that has silently stopped draining must be visible.

### Bucket configuration (document in the README; not code's job)

- **Enable versioning**, so an overwrite cannot silently replace an object.
- **Enable a Bucket Lock retention rule** matching the retention period.
  Note honestly in the README: R2's Bucket Locks provide retention policies
  but **not** compliance-mode immutability, legal hold, or SEC 17a-4
  certification. It prevents casual and accidental deletion; it is not
  courtroom-grade WORM. If that ever matters, S3 Object Lock in compliance
  mode is the option - but do not over-claim what R2 provides.
- **Scoped API token**: Object Read & Write, limited to this one bucket.
  Never an account-wide admin token.
- Credentials from environment or a `0600` file outside the repo. Never in
  config committed to git, never in the database.
- Optional lifecycle rule to expire objects past the retention window.

### Cost

R2 is $0.015/GB-month with zero egress, Class A (writes) at $4.50/million and
Class B (reads) at $0.36/million, and a free tier of 10 GB plus 1M Class A and
10M Class B operations. At `verified_only` video this workload is a few GB per
month and a few thousand operations - effectively free, and a dollar or two a
month even if it grows. Cost is not a design constraint here; do not add
complexity to optimize it.

## 11. Testing - build this first

I want to trust these numbers, so the verification comes before the
implementation.

- **`internal/dsp`**: table-driven test asserting A-weighting response at
  31.5 / 63 / 125 / 250 / 500 / 1000 / 2000 / 4000 / 8000 / 16000 Hz matches
  the IEC 61672 table within +/-0.5 dB. Drive the filter with generated sine
  waves and measure steady-state RMS gain.
- **Butterworth low-pass**: -3 dB at 500 Hz, >= 48 dB down at 2 kHz.
- **`internal/meter`**: a full-scale 1 kHz sine reads 94 dB SPL given the
  configured sensitivity. A 20 dB amplitude change produces a 20 dB LAeq
  change (linearity).
- **`internal/detect`**: synthetic signal generators - pink noise at a set
  level, a 2.5 Hz impulse train (-> `running`), isolated impulses 2 s apart
  (-> `jumping`), irregular impulses (-> `stomping`), steady noise
  (-> no event). Assert both detection and classification.
- **Baseline**: assert it tracks a slow ambient ramp without triggering.
- **`internal/store`**: migrations apply cleanly to an empty DB; review writes
  never alter `events` or `samples_1s` rows (assert by hashing before/after).
- **Pipeline**: end-to-end with a synthetic WAV as input, under `-race`.
- Add a `stompwatch verify-dsp` subcommand that runs the measurement assertions
  against the live configuration and prints a report - so I can re-verify on
  the actual hardware after any change.

## 12. Naming

The project is `stompwatch`. **Exports stay neutral.** Nothing the application
exports may carry a joke or a judgment - not the export filenames, chart
titles, CSV headers, or document metadata. They use instrument-style naming:
database `noise.db`, exports titled along the lines of "Impact noise log -
<date range>".

This is not fussiness. Exports go to a landlord, a mediator, or a code
enforcement officer, and an artifact carrying a joke name reframes a
measurement record as a campaign against a person. Check export filenames, CSV
headers, and PDF metadata specifically - those are where a stray name survives
unnoticed.

## 13. Explicitly out of scope

- No accelerometer or vibration sensor.
- No audio from the camera, ever.
- No use of the camera's own person/pet detection, motion detection, SD
  recording, or CGI recording commands for triggering - the dB detector
  decides what is an event.
- No hardcoded camera model, URL path, or credentials anywhere in the code.
- No login/user system, no multi-tenancy.
- No cloud services beyond the R2 replication in section 10. No telemetry, no
  auto-update, no third-party analytics, no external APIs in the measurement
  or detection path.
- No ML models. The classifier is deterministic signal processing I can
  explain to a non-engineer.

## 14. How to proceed

1. Scaffold the module and package layout. Commit.
2. Write `internal/dsp` tests from section 11, then implement until they pass. Commit.
3. Work outward: meter -> detect -> clip -> store -> capture. Tests first at each
   step.
4. When Phase 1 runs end-to-end against a synthetic input and `verify-dsp`
   passes, **stop and check in with me** before starting Phase 2.

### Open items - ask me, do not assume

- **`capture_channel`**: the mic presents stereo for one capsule. I have not
  yet confirmed whether both channels carry signal or only the left. Default to
  0 (left), make it configurable, and log the per-channel RMS at startup so a
  wrong choice is immediately visible rather than silent.
- **Calibration file format**: I have not yet confirmed the EM-01's calibration
  file is a plain REW-style text table. If it turns out to be proprietary, tell
  me before working around it.
- **Camera model and RTSP path**: not yet verified on hardware. Keep it
  configurable per section 7 and do not hardcode.

Ask me about anything ambiguous rather than guessing. If a requirement here
seems wrong, say so before implementing it - but do not quietly relax the
constraints in section 3.

## 15. Decisions

These decisions were made on 2026-09-11. Where they conflict with the text
above, these decisions apply.

1. **dBFS convention is AES17.** A full-scale sine is 0 dBFS, so the level in
   dBFS is `10*log10(mean(x^2)) + 3.01`. This replaces the section 11 meter test. A
   1 kHz sine at -13 dBFS peak must read 94.0 dB SPL. A full-scale sine must
   read 107.0 dB SPL. Confirm this with an acoustic calibrator on the real
   hardware.
2. **A-weighting test limits.** The bilinear transform at 48 kHz gives an error
   of -0.54 dB at 8 kHz and -6.43 dB at 16 kHz, so +/-0.5 dB is not possible
   there. The test uses +/-0.5 dB from 31.5 Hz to 4 kHz. At 8 kHz and 16 kHz it
   uses the IEC 61672-1 Class 1 limits (8 kHz: +2.1/-3.1 dB, 16 kHz:
   +3.5/-17.0 dB).
3. **Detector timing.** The detector runs every 100 ms on the Fast-weighted
   A level (LAF), not on 1-second bins. A candidate event opens at the first
   crossing of the threshold. The event is kept only if its total time above
   the threshold reaches `min_duration_ms` before it closes. Event times are
   epoch milliseconds, in columns `started_ms` and `ended_ms`. `samples_1s`
   stays at 1-second resolution.
4. **Clip filter outputs 1 kHz, not 2 kHz.** A 4th-order Butterworth only
   reduces the 500 Hz-1 kHz band (24 dB at 1 kHz), so a 2 kHz file can be
   boosted back with EQ. The clip path is:
   1. The 4th-order Butterworth low-pass at 500 Hz from section 6.4.
   2. A linear-phase FIR decimator, Kaiser window, from 48 kHz to 1 kHz.
      The passband edge is 400 Hz. The stopband starts at 500 Hz, which is
      the new Nyquist frequency. The stopband rejection is at least 90 dB.
   3. 16-bit PCM mono WAV at 1 kHz.

   After this path, the file cannot contain a signal above 500 Hz. The
   20-250 Hz impact band is not changed. The filter runs all the time, and
   the 30-second pre-roll buffer holds only filtered 1 kHz samples. Raw audio
   exists only in short-lived frames in memory. Clip timestamps include the
   group delay of the filters. This replaces "decimate to 2 kHz" in section 3.2,
   section 6.4, and section 6.8. It is not configurable and has no bypass.

   Tests: the Butterworth tests from section 11 stay. The full clip path must reject
   a sine at every tested frequency from 500 Hz to 24 kHz by at least 90 dB,
   including frequencies that alias into 0-500 Hz. It must pass 20-250 Hz
   within +/-0.5 dB.

   A 30-second clip at 1 kHz is 60 KB. The 60 KB figure in section 10 was the size
   at 1 kHz, not at 2 kHz.
5. **Clip playback (Phase 3).** Chrome does not play audio below 3 kHz
   (`media::limits::kMinSampleRate`). The server raises the sample rate for
   playback only, when it serves the clip. The stored file and its SHA-256 do
   not change.

The implementation of Phase 1 made these further decisions. They were not
checked with the owner in advance. Review them at the Phase 1 check-in.

6. **Time columns.** `samples_1s` and `samples_1m` stay keyed by epoch
   seconds. Every other time column is epoch milliseconds and ends in `_ms`:
   `events.started_ms`, `ended_ms`, `created_ms`; `event_review.reviewed_ms`;
   `mute_windows.start_ms`, `end_ms`; `config.updated_ms`;
   `system_health.ts_ms`.
7. **Schema additions.** `events.forced` (closed by `max_event_s`),
   `event_media.truncated`, and `event_review.reviewer` (for the audit trail
   in section 9.0). CHECK constraints limit `class`, `kind`, and `status` to their
   listed values. Triggers refuse UPDATE and DELETE on `samples_1s`, `events`,
   `event_media`, and `system_health`. A future retention job needs a
   migration that allows logged deletes.
8. **Goroutines and packages.** The meter and the detector run on the DSP
   goroutine instead of separate `meterLoop` and `detectLoop` goroutines. Both
   do constant work per sample and do not allocate, so the extra hand-offs
   would add nothing. The loops live in a new package, `internal/pipeline`.
   `internal/verify` holds the `verify-dsp` checks. `internal/testsignal`
   holds synthetic audio for tests only.
9. **Go version.** The minimum is Go 1.25, because `modernc.org/sqlite`
   v1.58 requires it.
10. **arecord arguments.** Capture adds `-q --buffer-time=200000
    --period-time=20000` to the arguments in section 6.1, to keep read latency and
    its jitter near 20 ms.
11. **Timestamps and dropped chunks.** Each chunk is stamped from the sample
    count, offset by the read with the least delay in a 30-second window.
    Each chunk carries its sample offset, so a dropped chunk is detected
    exactly. After a drop, the meter and the clip recorder start a new stream.
12. **Long events.** When a candidate event opens, the clip recorder holds
    audio from `pre_roll_s` before its start until the clip is saved, so
    events longer than the 30-second buffer keep their full clip. Clip paths
    use the UTC date of the clip start.
13. **Detection warm-up.** The detector needs `min_baseline_s` (default 30)
    seconds of baseline before it acts.
14. **Config.** The config file format is `key = value`. An unknown key, a
    repeated key, or a bad value is an error. Settings stored in the database,
    and reloading them without a restart, move to Phase 3, where the settings
    screen needs them.
15. **Logs.** Phase 1 logs JSON to stderr for journald. The rotating log file
    under `log_dir` moves to Phase 3, where the log view needs it.
16. **Stuck audio alerts.** A stuck stream sends a heartbeat `/fail` ping at
    once, at most once every 10 minutes. A stuck stream still delivers chunks,
    so without this the heartbeat would keep reporting success.
17. **calibrate.** `stompwatch calibrate` records 10 seconds by default,
    band-passes the signal from 500 Hz to 2 kHz, applies the calibration file,
    and prints the AES17 level to use as `sensitivity_dbfs`.

The owner made these decisions on 2026-09-12.

18. **The clip filter cutoff is a config file setting, and the owner has
    raised it to 1 kHz.** This overrides "do not make this configurable" in
    section 3.2 and in decision 4. section 3.2 and its reasoning stay where they are: the
    legal position has not changed, and the next person to read this file
    needs to know what was given up and why.

    *The owner's reason.* The microphone is inside their own condo. Only
    impact noise - running, stomping, jumping - comes through the ceiling.
    No conversation from the unit above is audible in the room. They were
    shown what raising the cutoff costs: at 1 kHz a clip holds twice the
    band, and speech in the owner's own room may become partly recoverable,
    though it stays far from intelligible. They chose the smallest step on
    the list.

    *What the setting is.* The config key is `clip_lowpass_hz`, in Hz. The
    default is 500, so a fresh install behaves exactly as decision 4
    describes. The allowed values are 500, 1000, 2000, 4000, 6000, 8000,
    and 12000. Each gives an output rate of twice the cutoff that divides
    48 000 exactly, so the decimation stays a whole number of samples. The
    stored WAV is at twice the cutoff. The FIR passband edge is 0.8 of the
    cutoff and its stopband edge is the cutoff, as at 500 Hz; the 95 dB
    Kaiser design and the 4th-order Butterworth do not change.

    *What is unchanged.* The filter still runs in the capture path, so an
    unfiltered clip cannot exist on disk, even transiently, even on crash.
    There is no bypass, no "unfiltered" mode, and no way to switch the
    filter off. The key is **not** on `settings.Editable`: changing what a
    recording may hold takes a config file edit and a restart, not a tap on
    a phone at 2am.

    *Consequences.* The collector logs a warning at WARN at every start
    while the cutoff is above the default, naming the value and saying that
    speech may be partly recoverable. `verify-dsp` measures the configured
    cutoff and names it in the report. A change applies only to clips
    recorded after the restart; clips already on disk keep their own rate,
    so a database holds clips of more than one kind, and the rate is in the
    media JSON as `rate_hz`. Playback picks its factor per clip. At 1 kHz a
    clip is 4 KB per second rather than 2 KB, and the collector's audio
    buffer is 2.9 MB rather than 1.4 MB.

    *Tests.* A filter at every allowed cutoff must reject everything above
    its own cutoff by at least 90 dB, up to 24 kHz, including frequencies
    that alias into the passband. Measured: at least 98.7 dB at every
    setting. A cutoff off the list is refused with a message that lists the
    choices. The startup warning appears above the default and not at it.

19. **The RTSP path list is a table of known cameras, and a path may be set
    by hand.** Section 7 names two default paths, `Preview_01_sub` and
    `h264Preview_01_sub`. Both are Reolink. The owner's camera is an
    Amcrest, an OEM build of Dahua: it answered every connection with
    `404 Not Found`, every few seconds, for as long as the collector ran. An
    unauthenticated RTSP `OPTIONS` to it returns a Digest realm that names
    the camera's Amcrest serial number. Section 7 stays as it is: it records what was believed when
    Phase 2 was built.

    *The list.* `video.KnownPaths` pairs each sub path with the main path of
    the same camera, so nothing derives one from the other. The two Reolink
    paths stay first, because those are the ones already in use.

    | Camera | Sub | Main |
    | --- | --- | --- |
    | Reolink, current firmware | `Preview_01_sub` | `Preview_01_main` |
    | Reolink, older firmware | `h264Preview_01_sub` | `h264Preview_01_main` |
    | Amcrest and Dahua | `cam/realmonitor?channel=1&subtype=1` | `cam/realmonitor?channel=1&subtype=0` |
    | Hikvision | `Streaming/Channels/102` | `Streaming/Channels/101` |
    | TP-Link Tapo | `stream2` | `stream1` |
    | Foscam | `videoSub` | `videoMain` |
    | Ubiquiti | `s1` | `s0` |

    This replaces "Main stream substitutes `_main` for `_sub`" in section 7.
    `camera_rtsp_path_main` empty now means "the main path in this camera's
    row". A sub path that is not in the table has no main path, and then
    `camera_rtsp_path_main` has to be set, as before.

    *Hand configuration must always be possible.* The list is a convenience,
    not a limit, and no list of seven covers every camera. The check on
    `camera_rtsp_path` was one plain segment of host characters, so a path
    with a `/`, a `?`, an `&`, or an `=` was refused: the owner could not set
    the right path even by hand, from the dashboard or in the config file.
    The check is now for a path ffmpeg can use: one or more segments
    separated by `/`, optionally followed by `?` and a query, with no leading
    slash. A scheme (`://`), whitespace or a control character, `..` or `.`
    as a segment, a backslash, a fragment (`#`), an empty segment, and `@`
    are each refused with a message that says which. `@` matters most: it
    would rewrite the userinfo of the URL the builder makes.
    `camera_host` is unchanged; a host is still a host.

    *The URL builder.* A query reaches ffmpeg unescaped. Put in the path of
    a `url.URL`, the `?` is written as `%3F` and the camera answers 404. The
    password is still masked in every printed, logged, and encoded form, on a
    URL with a query as on one without.

    *Cost.* A camera that answers none of the paths is retried on a backoff
    that doubles from 1 s to 60 s, one path per attempt, so a sweep of all
    seven takes about 63 s rather than the 1 s two took. Later sweeps take
    seven minutes, at the 60 s ceiling. The backoff is unchanged: a wrong
    path is found once, at bring-up, and then set.

20. **The first screen is called Live, not Tonight.** The nav item, the page
    heading, the route component, and the files under `web/src` all say
    Live. The route path stays `/`, so every link and the single-page
    fallback keep working.

    *Why.* The screen is the live meter, the trace of the window that is
    filling now, and the events in it. It is the screen the owner opens to
    see what is happening at this moment, and that is not only a night
    thing: a delivery at 11am belongs there too. "Tonight" named the window
    rather than the job.

    *What "tonight" still means.* The word is kept everywhere it means the
    actual night and not the screen. `nightWindow` in `web/src/lib/time.ts`
    still returns the title "Tonight" or "Last night", and that title is now
    shown beside the screen name rather than as it: the heading reads
    **Live**, and the line next to it reads "Tonight, Thu 11 Sep, 18:00 to
    now". The night rule, 18:00 to noon the next day, does not change, and
    the empty state still reads "No events tonight."

    Section 9.2 above is left as it was written. It records what the three
    screens were called when the interface was designed.

21. **The owner can delete recordings from disk. No row is ever deleted.**
    The owner asked to select events and delete both the recordings and the
    events, to reclaim the disk the video takes.

    *What was asked, and what was built.* Deleting the rows would have meant
    lifting the triggers on `events`, `event_media` and `samples_1s`, and it
    would have changed what the record claims: an event that is not in the
    database did not happen, as far as anybody reading it later can tell.
    The measurement is the thing this instrument exists to keep. So only the
    **files** are purged. The event row, its seconds, its review and its
    note all stay, and `event_media` keeps its row with the size, the
    duration and the SHA-256 of the recording that was there. That row is
    the record that a clip existed and what it was.

    *How a purge is recorded.* A new table, `media_purge`, holds one row per
    deleted file: the event, the kind, the bytes reclaimed, the instant, and
    the login of whoever asked. It carries the same immutability triggers as
    the other raw tables, so once a purge is recorded it cannot be
    unrecorded. Every purge request also writes one `media_purge`
    `system_health` row naming the events and the bytes, because rule 3 of
    section 3 says every deletion is logged. Nothing in section 3 changes:
    retention is still the only thing that deletes data, and a purge deletes
    no data, only the files the data describes.

    *Telling a purge from a loss.* A file that has vanished is a fault, and
    the dashboard has always answered 404 and written a `media_missing`
    health row for one. A purged clip answers **410 Gone** with the date and
    the person, and writes no health row: filling the health log with alarms
    about something the owner did on purpose would make the log useless. The
    media object in `GET /api/events/{id}` carries `purged_ms` and
    `purged_by`, and its `missing` flag stays false for a purged clip.

    *The interface.* The Events screen has a checkbox per row, a select-all
    for the page, and a bar that appears only while something is selected.
    Its button reads **Delete the recordings**, never "Delete": the event
    and the measurement stay, and the copy has to carry that or the owner
    will think the measurement went too. A confirmation panel names how many
    events and how many megabytes, says in one sentence what survives, and
    has its own button. The System screen has a Recordings section with what
    audio and video hold, what has already been reclaimed, and a purge by
    age that shows the count and the size before it runs.

    *Why the confirmation is in the request.* `POST /api/media/purge`
    refuses a body without `"confirm": true`. The confirmation is the one
    guard against an act with no undo, and a guard that lives only in the
    interface is a guard a stray fetch walks past.

    *Limits.* One request may name at most 500 events. A stored path that
    escapes its media root is refused rather than deleted, using the same
    containment check the clip handlers make. A file that is already gone is
    recorded as purged with 0 bytes, because what is recorded is the
    decision, not the free space. An id that is not an event never reaches a
    filesystem path.

22. **The camera may record its own audio, unfiltered, and the owner has
    switched it on.** The last rule of section 7 reads: "Never ingest camera
    audio. Force `-an` on every ffmpeg invocation." That rule stays where it
    is, and so does every rule in section 3. This decision overrides the
    first sentence of it for one setting, and records what was given up.

    *Why the rule existed.* Several US states and many other countries
    require the consent of everyone recorded. The camera's microphone is full bandwidth and
    uncalibrated, so its track is not a measurement of anything; it is a
    recording of a room. The measuring microphone's clips are low-pass
    filtered in the capture path, at `clip_lowpass_hz`, so speech in them
    is not intelligible. Camera audio has no such filter. It captures
    intelligible speech, which the microphone's own clips are deliberately
    built to exclude.

    *The owner's decision, 12 September 2026.* They chose to record the
    camera's audio unfiltered. They were shown what it costs: the track is
    full bandwidth and uncalibrated, the camera is inside their own home,
    and every video clip cut while the setting is on can hold intelligible
    speech that an audio clip of the same event cannot.

    *What the setting is.* The config key is `camera_audio`, true or false.
    The default is **false**, so a fresh install forces `-an` on every
    ffmpeg command line, exactly as section 7 says. Only `camera_audio =
    true` drops it, and it drops it from two command lines: the segment
    ring and the event-clip concat. The probe keeps `-an`, because it
    throws its output away; it reads the camera's audio track from the
    stream list ffmpeg prints about its input.

    *What is unchanged.* The measuring microphone's path is untouched.
    `internal/dsp`, `internal/meter`, `internal/clip` and `internal/audio`
    do not change, and an audio clip is still filtered at
    `clip_lowpass_hz` in the capture path, with no bypass. The audio track
    is recorded as the camera heard it: nothing filters it, and nothing
    trims it.

    *The picture is never re-encoded; the audio track is, at the ring.*
    This paragraph first read "nothing is re-encoded: the ring and the clip
    stay `-c copy`". That was wrong, and it cost the owner a day of
    recordings that held no sound at all.

    Measured on the owner's Amcrest camera with ffmpeg 7.1.5 on 12
    September 2026. Straight from RTSP the stream is `aac at 8000 Hz`, and
    the AAC configuration - sample rate, channel count, profile - arrives
    out of band, in the RTSP session description, never inside the stream.
    A copied track therefore reaches an MPEG-TS segment with
    `sample_rate=0` and `channels=0`. Decoding that segment fails with
    "unspecified sample format"; re-encoding it fails with "neither number
    of channels nor channel layout specified". The audio in such a segment
    cannot be recovered by anything, and the join dropped the track in
    silence and wrote a video-only clip.

    So the ring re-encodes the audio, where the configuration is still to
    hand: `-c:v copy -c:a aac` in place of `-c copy`, and each segment then
    describes its own audio. **The picture is still copied**, which is the
    property section 7 is about, and the clip concat is still `-c copy`.
    8 kHz mono AAC is negligible work on this box; a video re-encode is
    not. With `camera_audio` off nothing changed: `-an` is still forced and
    the one `-c copy` still covers everything.

    The concat also gained `-map 0`, so every stream of the joined input is
    taken and a stream that cannot be written fails the join. Without it
    ffmpeg chose streams by its own rules and dropped the rest without a
    word, and that silence is what hid this for a day.

    *Consequences.* The collector logs a warning at WARN at every start
    while the setting is on, naming the key and saying that the camera
    records speech in clear. The line that says the camera's audio is being
    recorded into the ring is read off the ring's own ffmpeg command line
    and not off the setting, because a line read off the setting is what
    claimed the audio was being recorded while every clip came out silent. The key **is** on `settings.Editable`, unlike
    `clip_lowpass_hz` in decision 18: it is not a credential and not a
    path, so the dashboard may change it, with `Live: false` because the
    ring and the clip writer are built once at start. The interface says
    what it does before it is switched on. A change applies only to video
    recorded after the restart; clips already on disk keep what they hold,
    so a database holds video clips of both kinds, and `camera_audio` in
    the media JSON says which a clip is.

    *A clip says what it holds, not what was asked for.* After each clip is
    written, the collector reads the file back and checks that the audio
    track is really there and gives its sample rate. `camera_audio` on the
    media row is that answer, so it is a fact about the file and not the
    setting in force when it was cut. A clip cut with the setting on that
    came out without a usable track gets a `clip_no_audio` row in
    `system_health` and an ERROR line naming the file. The clip itself is
    still kept: it is evidence of the event either way. The owner has to
    learn about a loss like this from the instrument, the way a truncated
    clip is already reported, and not by running `ffprobe` by hand.

    *Many cameras send no audio at all, and then the setting does nothing.*
    A setting that silently does nothing is a trap, so the probe reports
    whether the stream carries an audio track, with its codec and sample
    rate, in `stompwatch probe-camera` and in `POST /api/camera/probe`, and
    `GET /api/system` carries the same fact. With `camera_audio` on, the
    collector asks the camera once at start and logs at WARN when the
    stream carries no audio track.

    *Tests.* The ring and the concat command lines are tested in both
    directions: `-an` is present with the setting off, which is the
    default, and absent with it on. A test that only checked the new
    behavior would let the default regress in silence. The whole argument
    list of each is also written out literally, so a change to it has to be
    meant. Separately, an end-to-end test builds a source with real video
    and real audio, writes segments through the ring's own arguments, joins
    them through the clip writer's own, and reads the result back with
    `ffprobe`: the joined MP4 must carry an audio stream with a sample rate,
    and the video stream must still be the source's own. It skips where
    ffmpeg is missing, so it proves nothing off the box:

        go test ./internal/video -run RealFFmpeg -v

23. **The instrument states how sure it is, and every event cites the
    calibration in force when it was recorded.** Two faults share one root,
    and one table closes both.

    *The first fault.* Every level stompwatch reports is
    `dB SPL = dBFS - sensitivity_dbfs + 94`, so every one of them rests on
    that one constant. It is `-13.0`, taken from the Eversolo EM-01
    datasheet, which states **plus or minus 2 dB of per-unit tolerance**.
    The dashboard printed `71.8 dB` when the truth was somewhere between
    69.8 and 73.8, and nothing on screen said so. A number presented as
    exact and later shown to be 2 dB out discredits every other number
    beside it, so the instrument says it first.

    *The second, and the worse one.* The printed evidence sheet read the
    sensitivity and the calibration file from `GET /api/system`, which
    reports what is in force **now**. Reprinting an event from three months
    ago silently claimed today's calibration. The day the owner measures the
    real sensitivity, every reprinted older event would carry the new figure
    and be wrong, and the sheet is the document that leaves the building.

    *What was built.* Four config file keys record how the figure is known:
    `sensitivity_source` (`datasheet` or `measured`),
    `sensitivity_uncertainty_db`, `sensitivity_measured_on` and
    `sensitivity_reference`. `measured` requires the date; `datasheet`
    requires the date and the reference to be empty, so the two states
    cannot blur. `stompwatch calibrate` prints all four lines, stamped with
    the day it ran, so pasting what it prints leaves the file consistent.

    A new table, `capture_settings` (migration 6), is the history of what
    the instrument was: the sensitivity, its uncertainty, its source, the
    calibration file and its correction, and the capture device and channel.
    The collector writes a row at start **only when something differs from
    the newest row**, so a restart that changes nothing adds nothing and the
    table stays a history rather than a start-up log. It carries the same
    immutability triggers as the other raw tables (section 3 rule 3).
    `calibration_file` is the **base name only**: no row and no response
    names a place on disk.

    *An event older than the history says so.* `CaptureSettingsAt` returns
    `ErrNotFound` when the history does not reach back to an event, and
    `GET /api/events/{id}` answers `"capture": null`. The printed sheet then
    states that the settings in force at capture were not recorded, and that
    today's settings are deliberately left off. A fallback to the current
    settings would be the same lie in a new place, so there is none.

    *Where it appears.* One sentence, generated from the data and never
    written out in the interface: the System screen as its own group beside
    Capture, the printed sheet from the **event's own** block, the event
    detail beside the measured levels, the PNG caption, and the CSV as the
    columns `level_uncertainty_db` and `sensitivity_source` on every row. A
    CSV has no comment syntax, so a column is the honest way; both cells are
    empty for an event older than the history.

    *Not editable from the dashboard.* None of the five calibration keys is
    on `settings.Editable`, and none will be. They do not tune the
    instrument; they describe how it was calibrated. A web form that could
    set `sensitivity_source` to `measured`, with a date and a reference of
    its own choosing, could claim a calibration that never happened.

    *What is unchanged.* Nothing about the measurement. `internal/dsp`,
    `internal/meter`, `internal/detect`, `internal/clip` and
    `internal/audio` are untouched. This records what was already true; it
    does not alter a single level.

24. **The owner can pause the recording at the same hours every day.** The
    owner's own television and conversation set off events, and the camera
    records them with sound. The owner asked for a window in which neither
    audio nor video is recorded.

    *What was built.* One config key, `recording_pause`, holds a span such as
    `19:00-23:00` in local time. It may wrap past midnight. Empty means no
    pause. It is one key and not two, because two keys cannot tell an unset
    end from midnight. The dashboard can change it, and it applies without a
    restart. Since 2026-09-14 the key holds one or more windows separated by
    commas; the owner's television is on at more than one time of day. The
    windows must not overlap or touch: the parser refuses a value where they
    do, naming the two windows, because Window can only say which one is
    active if at most one ever contains a given moment.

    *Inside the pause.* The detector is given no frames, so no event opens and
    no clip is asked for. An event still open when the pause begins is closed
    at its last loud moment, and it is stored at once rather than held open
    for the whole pause. Every clip window is cut back at the edges of the
    pause: a post-roll stops where the pause begins, and a pre-roll starts
    where it ended. The camera ring runs no ffmpeg. It stops one poll before
    the pause begins and starts again only when the pause is over.

    *What is not paused.* The level of every second. Section 3 rule 4 names a
    silent loss of a night of data as the worst failure, and a pause must not
    look like one. The meter keeps measuring and `samples_1s` keeps its rows,
    so the timeline has no hole. Each start and end of the pause is a
    `recording_pause` row in the health log, so a stretch with no events has a
    reason. A stop for the pause is not counted as a camera disconnect and
    opens no video outage.

    *Limits, stated.* The audio recorder still holds the last 90 seconds of
    filtered sound in memory during the pause, as it always does. None of it
    is written to disk. Noise from upstairs inside the pause is not recorded
    as an event, so it cannot be reviewed or exported. That is the trade the
    owner chose. The television is not on at fixed hours, so a manual pause
    may be wanted later; the pause is built so that one can reuse it.

25. **Events are labeled by how fast they rose, not only by their
    envelope.** On 2026-09-14 the box recorded 406 events in a night; the
    owner rejected 19 of the 20 reviewed. The air conditioner lifts the room
    from 27 to 41 dB and holds it; the 10-minute baseline lags, so every
    flutter of the fan stood 15 dB over it. The band ratio did not help: the
    fan is low-frequency, +13 to +17 dB, the same side as a hit. The stored
    envelope did not help either: the fan flutters in the 20-120 Hz band.

    *What separates them.* The level just before the event and the speed of
    the rise. The one verified hit rose 17 dB over the 30 s before it and
    13 dB within one second. The fan starting rose 15 dB but 4 dB per second.
    The television sat at a ratio of -3 to -17 dB. Rules: ratio <= 0 dB is
    airborne; a jump under 6 dB or a rise under 8 dB is steady; a short
    event with no cadence is an impact. Over that night: 299 airborne, 81
    steady, 19 impact, 7 with the old labels. All 32 labels sort correctly.
    The thresholds rest on one verified hit and will move as labels arrive.

    *Revised 2026-09-15 on 51 labels* (14 verified, 37 rejected). The rise
    gate at 8 dB missed three verified events. Two were 0.4 s hits: a hit
    that short shares its second with 0.6 s of quiet, so its one-second
    rise reads 6-7 dB. The gate moved to 5 dB; the closest rejected event,
    the fan starting, reads 4.0. The third was a 7 s furniture drag that
    rose 3.5 dB per second, like the fan, but whose peak stood 7 dB over its
    own mean where the fan's stands 2.5 dB at most. So a slow rise is steady
    only when the crest (LAmax minus LAeq) is also under 5 dB. With that,
    46 of 51 labels sort correctly. The five that do not: four rejected
    events longer than 10 s, which the old cadence rules still label; and
    one verified 3 s event 5 dB over a 38 dB room, which nothing measured
    separates from a rejected event 4 dB over the same room.

    *What is stored.* `jump_db` and `rise_db` on every new event, NULL on
    old rows and on events with less than 10 s of levels before them. The
    envelope alone cannot recompute them, so they are stored, not derived.

    *What is not done.* Old events keep their labels: event rows are
    immutable (section 3 rule 3). The trigger is unchanged: every event is
    still stored. A shorter `baseline_window` (3 min) removes most of the
    fan events at the source; the owner sets it in the config file.

26. **The camera host and port are set in the config file only.** The
    dashboard could set `camera_host` and `camera_port`. The camera login
    goes to that host: the camera test connected at once to the new value,
    and the collector would too after a restart. So anyone who could open
    the dashboard could set the host to a machine of their own, press Test,
    and receive the password from ffmpeg.

    *What changed.* The two keys are off `settings.Editable`, for the reason
    `camera_credentials_file` is: a web form must not point the collector at
    any file it likes to read, and it must not send the password to any host
    it likes. `PUT /api/settings` refuses them. The System screen shows the
    address read-only and says it comes from the config file.

    *A row stored earlier.* `settings.New` takes any config key from the
    `config` table, so a row the dashboard wrote before this change would
    still have been used. Now `settings.New` refuses these two keys, and the
    collector takes them out at start with `settings.DropFileOnly`. It logs
    an error, writes a `settings_change` row to `system_health`, and deletes
    the row, so the report comes once. The other stored settings still apply.

27. **An API request with no login is refused, and so is Funnel.** In
    `tailscale` and `trusted_header` mode a request with no login header was
    served as `unknown`, with full rights. The reason was that the network
    had already authenticated the caller, and refusing would lock the owner
    out. But a request with no login header did not come through
    `tailscale serve` or the proxy, so nothing authenticated it. This
    reverses the rule in docs/http-api.md "Identity".

    *What changed.* An `/api` request with no login header, or an empty one,
    gets 401. The message says to open the dashboard through
    `tailscale serve` or the proxy, or to use `auth_mode = none` on loopback
    for an SSH tunnel. The page and its files still load, so the owner can
    read the message. `tailscale serve` sets no login for a tagged node, so
    such a node gets 401 too. `auth_mode = none` is unchanged, and
    `tools/devserver` uses it.

    *Funnel.* `tailscale serve` sets `Tailscale-Funnel-Request: ?1` on a
    request that came from the public internet through Funnel, sets no login
    on it, and deletes the header from every other request
    (tailscale/tailscale, `ipn/ipnlocal/serve.go`,
    `addTailscaleIdentityHeaders`). Such a request gets 403 in every mode,
    the page included. The collector already reports Funnel as an error at
    start; this stops it from answering meanwhile.
