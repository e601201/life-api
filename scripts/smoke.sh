#!/usr/bin/env bash
# 打鍵確認をまとめて流す。README の「打鍵確認（curl）」と同じ内容を、ステータスコードで判定する。
#
#   bash scripts/smoke.sh                                   # http://localhost:8080
#   bash scripts/smoke.sh "$(terraform -chdir=terraform output -raw api_url)"
#
# 実行のたびに新しいユーザを 2 人登録する（email に時刻を入れるので、何度流しても 409 にならない）。
# 作った記録とタグは最後に消すが、ユーザは残る（削除の API が無い）。curl と jq が要る。
set -u

BASE="${1:-http://localhost:8080}"
STAMP="$(date +%s)"
PASS=0
FAIL=0

# req <期待するステータス> <説明> <curl の引数...>。ボディは $BODY に残す。
req() {
  local want="$1" name="$2"
  shift 2
  local out code
  out="$(curl -s -m 10 -w '\n%{http_code}' "$@")"
  code="${out##*$'\n'}"
  BODY="${out%$'\n'*}"
  if [ "$code" = "$want" ]; then
    PASS=$((PASS + 1))
    printf 'ok   %s %s\n' "$code" "$name"
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL %s (want %s) %s\n     %s\n' "$code" "$want" "$name" "$BODY"
  fi
}

# check <説明> <条件>。ボディの中身を見るとき用。
check() {
  if [ "$2" = "true" ]; then
    PASS=$((PASS + 1))
    printf 'ok       %s\n' "$1"
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL     %s\n     %s\n' "$1" "$BODY"
  fi
}

JSON=(-H 'Content-Type: application/json')
EMAIL="smoke-${STAMP}@example.com"
EMAIL2="smoke-${STAMP}-other@example.com"

echo "== ${BASE}"

req 200 "health" "${BASE}/health"

# users
req 201 "登録" "${JSON[@]}" "${BASE}/users" -d "{\"email\":\"${EMAIL}\",\"password\":\"correct horse\"}"
req 409 "登録済みの email（大文字違い）" "${JSON[@]}" "${BASE}/users" \
  -d "{\"email\":\"SMOKE-${STAMP}@example.com\",\"password\":\"another one\"}"
req 200 "ログイン" "${JSON[@]}" "${BASE}/users/login" -d "{\"email\":\"${EMAIL}\",\"password\":\"correct horse\"}"
TOKEN="$(printf '%s' "$BODY" | jq -r .access_token)"
AUTH=(-H "Authorization: Bearer ${TOKEN}")
req 401 "ログイン失敗（パスワード違い）" "${JSON[@]}" "${BASE}/users/login" \
  -d "{\"email\":\"${EMAIL}\",\"password\":\"wrong password\"}"
req 200 "自分の情報" "${AUTH[@]}" "${BASE}/users/me"

# 認証の異常系
req 401 "トークン無し" "${BASE}/entries"
req 401 "不正なトークン" -H 'Authorization: Bearer nope' "${BASE}/entries"

# entries
req 201 "記録の作成（タグ付き）" "${AUTH[@]}" "${JSON[@]}" "${BASE}/entries" \
  -d "{\"title\":\"smoke ${STAMP}\",\"entry_date\":\"2026-09-01\",\"kind\":\"til\",\"body\":\"needle-${STAMP}\",\"tags\":[\"smoke-a-${STAMP}\",\"smoke-b-${STAMP}\"]}"
ID="$(printf '%s' "$BODY" | jq -r .id)"
req 200 "一覧" "${AUTH[@]}" "${BASE}/entries"
req 200 "単体取得" "${AUTH[@]}" "${BASE}/entries/${ID}"
req 404 "存在しない id" "${AUTH[@]}" "${BASE}/entries/999999999"

# 検索
req 200 "検索: タグ 2 つの AND" "${AUTH[@]}" "${BASE}/entries?tag=smoke-a-${STAMP}&tag=smoke-b-${STAMP}"
check "  → 1 件" "$(printf '%s' "$BODY" | jq 'length == 1')"
req 200 "検索: 部分一致（大文字小文字を区別しない）" "${AUTH[@]}" "${BASE}/entries?q=NEEDLE-${STAMP}"
check "  → 1 件" "$(printf '%s' "$BODY" | jq 'length == 1')"
req 200 "検索: 期間（両端を含む）" "${AUTH[@]}" "${BASE}/entries?from=2026-09-01&to=2026-09-01"
check "  → 1 件" "$(printf '%s' "$BODY" | jq 'length == 1')"
req 200 "検索: 期間の外" "${AUTH[@]}" "${BASE}/entries?from=2026-09-02"
check "  → 0 件" "$(printf '%s' "$BODY" | jq 'length == 0')"

# 他人の記録は 404（403 ではない）
req 201 "2 人目の登録" "${JSON[@]}" "${BASE}/users" -d "{\"email\":\"${EMAIL2}\",\"password\":\"correct horse\"}"
req 200 "2 人目のログイン" "${JSON[@]}" "${BASE}/users/login" -d "{\"email\":\"${EMAIL2}\",\"password\":\"correct horse\"}"
TOKEN2="$(printf '%s' "$BODY" | jq -r .access_token)"
req 404 "他人の id は 404" -H "Authorization: Bearer ${TOKEN2}" "${BASE}/entries/${ID}"

# 更新（PUT は全置換。tags を 1 つに減らす）
req 200 "更新" -X PUT "${AUTH[@]}" "${JSON[@]}" "${BASE}/entries/${ID}" \
  -d "{\"title\":\"smoke ${STAMP} (更新)\",\"entry_date\":\"2026-09-01\",\"kind\":\"til\",\"tags\":[\"smoke-a-${STAMP}\"]}"
check "  → tags が 1 つ、body が null" "$(printf '%s' "$BODY" | jq '(.tags | length == 1) and (.body == null)')"

# tags
req 200 "タグの一覧" "${AUTH[@]}" "${BASE}/tags"
TAG_A="$(printf '%s' "$BODY" | jq -r ".[] | select(.name == \"smoke-a-${STAMP}\") | .id")"
TAG_B="$(printf '%s' "$BODY" | jq -r ".[] | select(.name == \"smoke-b-${STAMP}\") | .id")"
req 200 "タグの改名" -X PUT "${AUTH[@]}" "${JSON[@]}" "${BASE}/tags/${TAG_A}" -d "{\"name\":\"smoke-c-${STAMP}\"}"
req 409 "既にある名前への改名" -X PUT "${AUTH[@]}" "${JSON[@]}" "${BASE}/tags/${TAG_A}" -d "{\"name\":\"smoke-b-${STAMP}\"}"
req 204 "タグの削除" -X DELETE "${AUTH[@]}" "${BASE}/tags/${TAG_A}"
req 204 "タグの削除（紐付けの無いタグ）" -X DELETE "${AUTH[@]}" "${BASE}/tags/${TAG_B}"

# バリデーション
req 400 "必須項目が無い" "${AUTH[@]}" "${JSON[@]}" "${BASE}/entries" -d '{"title":"x"}'
req 400 "空の title と不正な日付" "${AUTH[@]}" "${JSON[@]}" "${BASE}/entries" \
  -d '{"title":"","entry_date":"banana","kind":"til"}'

# 片付け
req 204 "記録の削除" -X DELETE "${AUTH[@]}" "${BASE}/entries/${ID}"
req 404 "削除後の取得" "${AUTH[@]}" "${BASE}/entries/${ID}"

echo "== ok ${PASS} / FAIL ${FAIL}"
[ "$FAIL" -eq 0 ]
