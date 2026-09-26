# Security

## Reporting a problem

Please report a security problem privately, through GitHub's
"Report a vulnerability" button on the Security tab of this repository
(private vulnerability reporting). Do
not open a public issue for it. Say what you found, how to reproduce it, and
what an attacker could do with it.

## The security model

StompWatch runs on a box in a home and records sound and, optionally, video.
Read this before you expose it to anything.

- **The dashboard has no login.** It listens on `127.0.0.1` by default. The layer
  in front of it (`tailscale serve`, a reverse proxy that authenticates
  users, or an SSH tunnel) decides who may reach it, and StompWatch reads the
  login that layer adds. Anyone who can reach the listener directly can act
  as any user.
- **Never publish the dashboard to the internet.** StompWatch refuses
  requests that arrive through Tailscale Funnel.
- **Write requests from other web sites are refused**, bodies must be JSON,
  and the dashboard cannot be framed.
- **The camera password** lives in a mode 0600 file read by the service. It
  is never in the config file, the database, the logs, or the dashboard,
  and it is removed from the environment after it is read. It is on
  ffmpeg's command line, which other local users can read unless `/proc` is
  mounted with `hidepid`. See the README.
- **Recordings and the database** are readable by the service user only.
- **Raw measurements are immutable.** The database refuses changes to the
  measurement, event, media, and health tables. Clip files are deleted only
  by retention, by a purge from the dashboard, or by `stompwatch reset`, and
  every deletion is recorded.

Known limits:

- Any local process on the box can call the loopback listener and claim any
  login. Keep the box single-user.
- RTSP to the camera is not encrypted on the local network. Put the camera
  on a network segment that only the box can reach, if you can.
