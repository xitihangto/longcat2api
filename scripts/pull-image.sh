#!/usr/bin/env bash
# 逐个尝试镜像加速站，拉到镜像后把可用的 IMAGE 写回 .env
#
#   ./scripts/pull-image.sh               # 用 .env 里的 TAG（默认 latest）
#   TAG=sha-1a2b3c4 ./scripts/pull-image.sh
#
# 想自己指定顺序就改 MIRRORS 数组。
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

REPO="xitihangto/longcat2api"
TAG="${TAG:-latest}"
ENV_FILE="${ENV_FILE:-.env}"

# 顺序即优先级：官方 → 国内加速（实测可拉）→ 备选
MIRRORS=(
  "ghcr.io"
  "ghcr.1ms.run"
  "ghcr.nju.edu.cn"
  "ghcr.dockerproxy.net"
)

if ! command -v docker >/dev/null 2>&1; then
  echo "✗ 未找到 docker 命令" >&2
  exit 1
fi

ok_mirror=""
for m in "${MIRRORS[@]}"; do
  ref="$m/$REPO:$TAG"
  printf '→ 尝试 %-26s ' "$m"
  if out=$(docker pull "$ref" 2>&1); then
    echo "✔"
    ok_mirror="$m"
    break
  fi
  echo "✗ ($(printf '%s' "$out" | tail -1))"
done

if [ -z "$ok_mirror" ]; then
  echo
  echo "✗ 所有加速站都失败了。可以：" >&2
  echo "  1) 配代理后重试：HTTPS_PROXY=http://127.0.0.1:7890 ./scripts/pull-image.sh" >&2
  echo "  2) 本地构建：  docker compose up -d --build" >&2
  exit 1
fi

image="${ok_mirror}/${REPO}"
echo
echo "✔ 可用镜像：${image}:${TAG}"

# 写回 .env（存在则替换 IMAGE=，不存在则追加）
if [ -f "$ENV_FILE" ]; then
  if grep -qE '^[[:space:]]*#?[[:space:]]*IMAGE=' "$ENV_FILE"; then
    tmp=$(mktemp)
    awk -v img="$image" '
      /^[[:space:]]*#?[[:space:]]*IMAGE=/ && !done { print "IMAGE=" img; done=1; next }
      { print }
    ' "$ENV_FILE" > "$tmp" && mv "$tmp" "$ENV_FILE"
  else
    printf '\nIMAGE=%s\n' "$image" >> "$ENV_FILE"
  fi
  echo "✔ 已写入 $ENV_FILE: IMAGE=$image"
else
  echo "提示：还没有 $ENV_FILE，可执行 cp .env.example .env 后重跑，或直接 export IMAGE=$image"
fi

echo
echo "下一步：docker compose up -d"
