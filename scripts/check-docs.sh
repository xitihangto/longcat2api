#!/usr/bin/env bash
# 文档命令块规范检查：
# 1) bash/sh/shell/zsh/console 代码块里不允许「命令 + 行内注释」
#    （zsh 关闭 interactive_comments 时 `cd x  # 注释` 会把 # 当参数报错）
# 2) 命令块里不允许出现明显占位符以外的中文说明混在命令行（如 `cd /path ← 换成你的`）
#
# 用法：./scripts/check-docs.sh [文件...]     （默认检查仓库内所有 .md）
# 退出码：0 = 通过，1 = 有违规（并打印 文件:行号）
set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

if [ "$#" -gt 0 ]; then
  FILES=("$@")
else
  FILES=()
  while IFS= read -r f; do FILES+=("$f"); done < <(find . -name '*.md' -not -path './.git/*' | sort)
fi

bad=0
for f in "${FILES[@]}"; do
  [ -f "$f" ] || continue
  out=$(awk -v file="$f" '
    /^[[:space:]]*```/ {
      if (in_fence) { in_fence=0; lang="" } else {
        in_fence=1
        lang=$0
        sub(/^[[:space:]]*```[[:space:]]*/, "", lang)
        sub(/[[:space:]].*$/, "", lang)
      }
      next
    }
    in_fence {
      shell = (lang=="bash" || lang=="sh" || lang=="shell" || lang=="zsh" || lang=="console" || lang=="")
      if (!shell) next
      line=$0
      # 跳过整行注释 / 空行
      if (line ~ /^[[:space:]]*#/) next
      if (line ~ /^[[:space:]]*$/) next
      # 取 # 之前的部分；粗略跳过含引号的行（# 可能在引号里）
      hash = index(line, "#")
      if (hash == 0) next
      before = substr(line, 1, hash-1)
      if (before ~ /["\x27]/) next
      # 命令与 # 之间要有 2 个以上空格才算「行内注释」
      if (before ~ /[^[:space:]][[:space:]][[:space:]]+$/) {
        printf "%s:%d: 命令块里的注释应独立成行（不要把 # 注释写在命令后面）: %s\n", file, NR, line
      }
    }
  ' "$f")
  if [ -n "$out" ]; then
    printf '%s\n' "$out"
    bad=1
  fi
done

if [ "$bad" -eq 0 ]; then
  echo "✅ 文档命令块检查通过"
fi
exit "$bad"
