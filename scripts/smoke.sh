#!/usr/bin/env bash
# 冒烟测试：健康检查 → 模型列表 → 非流式 → 流式
set -uo pipefail
BASE="${BASE:-http://127.0.0.1:8090}"
KEY="${KEY:-}"
H=(-H "Content-Type: application/json")
[ -n "$KEY" ] && H+=(-H "Authorization: Bearer $KEY")

echo "== healthz ==";  curl -sS --max-time 10 "$BASE/healthz"; echo
echo "== models ==";   curl -sS --max-time 10 "${H[@]}" "$BASE/v1/models"; echo
echo "== chat (non-stream) =="
curl -sS --max-time 120 "${H[@]}" "$BASE/v1/chat/completions" \
  -d '{"model":"longcat","messages":[{"role":"user","content":"只回复：pong"}],"stream":false}'; echo
echo "== chat (stream) =="
curl -sS -N --max-time 120 "${H[@]}" "$BASE/v1/chat/completions" \
  -d '{"model":"longcat","messages":[{"role":"user","content":"从1数到3"}],"stream":true}' | tail -8
echo
