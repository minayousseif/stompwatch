// Package logs writes the program's JSON log to a rotating file and reads it
// back for the dashboard.
//
// Writer is the destination of a slog.NewJSONHandler. It writes whole records
// only: a record is never split across two files, however big it is.
//
// Read scans the files from the newest backwards, in blocks from the end, so
// showing the last 200 lines of a 20 MB log does not read 20 MB. A line that
// is not valid JSON comes back as RAW rather than being dropped. A log that
// hides the thing that went wrong is worse than no log.
package logs
