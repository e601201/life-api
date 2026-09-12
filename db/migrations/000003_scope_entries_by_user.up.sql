-- entries を user_id でスコープする（life#43）。以降、記録は必ず誰かのもの。

-- users より前に作られた行は user_id が NULL で、スコープを入れると誰からも見えなくなる。
-- 本番の RDS は削除済みで、次に作るときは空の DB に全マイグレーションが順に流れるので
-- 対象は無い。残っているのはローカルの curl での試し打ちだけなので、NOT NULL を付ける前に消す。
DELETE FROM entries WHERE user_id IS NULL;

ALTER TABLE entries ALTER COLUMN user_id SET NOT NULL;

-- 一覧は「自分の記録を記録日の新しい順」になったので、user_id を先頭に置いた
-- インデックスに差し替える。旧 entries_entry_date_idx は user_id で絞る一覧では
-- 使われないので落とす（id を第 3 キーに入れる理由は 000001 と同じ）。
CREATE INDEX entries_user_id_entry_date_idx ON entries (user_id, entry_date DESC, id DESC);
DROP INDEX entries_entry_date_idx;
