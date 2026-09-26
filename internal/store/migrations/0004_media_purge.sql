-- Schema version 4. Record the media files the owner deliberately deleted.
--
-- The owner asked to delete recordings and reclaim the disk they take. Rows
-- are immutable (SPEC.md section 3 rule 3), so only the files go: the event,
-- its seconds, its review, and its event_media row all stay. event_media
-- keeps the size, the duration and the SHA-256, because that row is the
-- record that the clip existed and what it was. See SPEC.md section 15
-- decision 21.
--
-- A row here is what makes a deleted file different from a lost one. Without
-- it a purged clip would read as a vanished clip, which is a fault: the
-- handlers would answer 404 and fill the health log with alarms about
-- something the owner did on purpose. With it they answer 410 Gone and say
-- when it went and who did it.
--
-- bytes is what the file took when it was deleted, and is 0 for a file that
-- was already gone. The purge is still recorded then, because what is being
-- recorded is the owner's decision, not the free space.
CREATE TABLE media_purge (
  event_id   INTEGER NOT NULL REFERENCES events(id),
  kind       TEXT NOT NULL CHECK (kind IN ('audio', 'video')),
  bytes      INTEGER NOT NULL,
  purged_ms  INTEGER NOT NULL,
  purged_by  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (event_id, kind)
);

-- The same immutability as the other raw tables. Once a purge is recorded it
-- cannot be unrecorded, so nothing can make the database claim a recording
-- was never deleted.
CREATE TRIGGER media_purge_no_update BEFORE UPDATE ON media_purge
BEGIN SELECT RAISE(ABORT, 'media_purge rows are immutable'); END;
CREATE TRIGGER media_purge_no_delete BEFORE DELETE ON media_purge
BEGIN SELECT RAISE(ABORT, 'media_purge rows are immutable'); END;
