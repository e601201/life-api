-- up で消した user_id が NULL の行は戻らない。
CREATE INDEX entries_entry_date_idx ON entries (entry_date DESC, id DESC);
DROP INDEX entries_user_id_entry_date_idx;
ALTER TABLE entries ALTER COLUMN user_id DROP NOT NULL;
