-- entries: TIL / 日誌の1件。design/design.go の Journal 型に対応する。
CREATE TABLE entries (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- user_id は W3 のマルチユーザ化で JWT から入る。users テーブルを作るまでは
    -- 参照先が無いので FK は張らず、NULL を許しておく。
    user_id    BIGINT,
    entry_date DATE        NOT NULL,
    kind       TEXT        NOT NULL,
    title      TEXT        NOT NULL,
    body       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- DSL の Enum("til","diary") と MinLength(1) を DB 側にも置く。API を通らない
    -- 経路（psql での直接投入、将来のバッチ）でもデータが壊れないようにするため。
    CONSTRAINT entries_kind_check  CHECK (kind IN ('til', 'diary')),
    CONSTRAINT entries_title_check CHECK (length(title) > 0)
);

-- 一覧は記録日の新しい順に返すので、その並びのまま引けるようにしておく。
-- id を第2キーに入れているのは、同じ日のエントリの順序を安定させるため。
CREATE INDEX entries_entry_date_idx ON entries (entry_date DESC, id DESC);
