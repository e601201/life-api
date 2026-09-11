-- users: ログインするユーザ。design/design.go の User 型に対応する。
CREATE TABLE users (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         TEXT        NOT NULL,
    -- bcrypt のハッシュ（"$2a$10$..." の 60 文字）。平文は持たない。
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT users_email_check CHECK (length(email) > 0)
);

-- email は大文字小文字を区別せずに一意にする（Foo@example.com と foo@example.com は同じ人）。
-- ログイン時の検索も lower(email) で引くので、このインデックスがそのまま効く。
-- API を通らない経路（psql での直接投入）でも重複しないよう、DB 側に置く。
CREATE UNIQUE INDEX users_email_lower_idx ON users (lower(email));

-- entries.user_id の参照先ができたので FK を張る。000001 で先送りにしていたもの。
-- users より前に作られた行は user_id が NULL のままで、FK は NULL を通す。
ALTER TABLE entries
    ADD CONSTRAINT entries_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id);
