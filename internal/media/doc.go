// Package media is what the dashboard and the retention job share about
// clip files on disk: how a stored path is turned into a file under its
// root, how one file is deleted, and how a deletion is described in the
// health log.
//
// It is one package so that the two things that delete a recording, the
// owner's purge and retention, cannot drift apart on the one check that
// matters: a stored path that leads outside its media root is refused,
// never deleted. A retention job with a path bug deletes the owner's
// evidence, and the check that stops it must be the check the purge has
// already proved.
//
// Nothing here touches a database row. A deletion is always of a file, and
// the record of it is written by the caller (SPEC.md section 3 rule 3 and
// section 15 decision 21).
package media
