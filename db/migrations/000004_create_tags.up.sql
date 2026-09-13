-- tags: ユーザごとのタグ。design/design.go の Tag 型に対応する（life#44）。
-- entries の tags（名前の配列）から暗黙に作られ、tags サービスでは改名と削除だけできる。
CREATE TABLE tags (
    id         BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    BIGINT      NOT NULL REFERENCES users (id),
    name       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- DSL の MinLength(1) / MaxLength(50) / Pattern（前後に空白なし）を DB 側にも置く。
    CONSTRAINT tags_name_check CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 50),
    -- 同じユーザの中で名前は一意。entries から名前で upsert するときの ON CONFLICT の対象で、
    -- 一覧を名前順に返すときのインデックスにもなる。大文字小文字は区別する。
    CONSTRAINT tags_user_id_name_key UNIQUE (user_id, name)
);

-- entry_tags: entries と tags の多対多。どちらを消しても紐付けは一緒に消える。
CREATE TABLE entry_tags (
    entry_id BIGINT NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
    tag_id   BIGINT NOT NULL REFERENCES tags (id) ON DELETE CASCADE,

    PRIMARY KEY (entry_id, tag_id)
);

-- 主キーは entry_id が先頭なので「記録のタグ一覧」には効くが、「タグの付いた記録」には効かない。
-- タグ検索（#45）と tags 一覧の件数で逆向きに引くので、そちらのインデックスも張る。
CREATE INDEX entry_tags_tag_id_idx ON entry_tags (tag_id, entry_id);
