-- FK を先に外さないと users を落とせない。entries の行と user_id の値は残る。
ALTER TABLE entries DROP CONSTRAINT entries_user_id_fkey;
DROP TABLE users;
