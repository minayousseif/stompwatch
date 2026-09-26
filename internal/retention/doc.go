// Package retention deletes the clip files of old events, and nothing
// else, to give the disk back.
//
// What it deletes is always a file: the audio and the video recording of
// an event that started more than retention_days ago. What it never
// deletes is a row. The event, its seconds, its levels, its class and its
// review all survive retention, and the event_media row keeps the size, the
// duration and the SHA-256 of the recording that was there. The database
// refuses UPDATE and DELETE on those tables, and this package never asks
// (SPEC.md section 3 rule 3 and section 6.9.1). samples_1s is never
// pruned: at about 1.2 GB a year it is the measurement, and clips are what
// take the space.
//
// Every deletion is recorded in media_purge with purged_by = "retention",
// so the dashboard answers 410 Gone with the date for a clip retention took,
// rather than reporting a missing file as a fault. Each run that deleted
// something writes one media_purge health row. A run that deleted nothing
// writes nothing.
//
// The job uses the same file mechanics as the owner's purge (the media
// package): a stored path that leads outside its media root is refused,
// never deleted. A file that will not delete is one logged line and a
// skipped file, not a crash. The job never holds a write transaction while
// it walks the disk: the files go first, then the purges are recorded in
// one transaction.
package retention
