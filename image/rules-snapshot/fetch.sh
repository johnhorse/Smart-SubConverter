#!/bin/sh
# 拉取 ACL4SSR 三档预设 .ini 及其引用的全部规则集文件，平铺落本目录（保留上游 basename），
# 写 SNAPSHOT_META（拉取时间 / 上游 HEAD commit / 每文件 sha256）。
# 用途：初版 vendor、人工刷新、upstream-watch workflow 漂移重拉，三者共用本脚本。
# 依赖：curl、gh（gh api 取上游 commit sha）、sha256sum 或 shasum（macOS）
set -eu

cd "$(dirname "$0")"

REPO=ACL4SSR/ACL4SSR
BRANCH=master
BASE_URL="https://raw.githubusercontent.com/${REPO}/${BRANCH}"

if command -v sha256sum >/dev/null 2>&1; then
  SHACMD=sha256sum
else
  SHACMD="shasum -a 256"
fi

# 清空旧快照数据（保留本脚本自身），上游删除文件时不留残迹
find . -maxdepth 1 -type f ! -name fetch.sh -delete

echo "== 拉取三档预设 .ini =="
for rel in \
  Clash/config/ACL4SSR_Online.ini \
  Clash/config/ACL4SSR_Online_Full_MultiMode.ini \
  Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini
do
  curl -fsSL --retry 3 -o "$(basename "$rel")" "${BASE_URL}/${rel}"
done

# 解析 .ini 中 URL 形态的 ruleset 行（非 URL 的 [] 内置型不落盘），去重
urls=$(grep -h '^ruleset=' *.ini | grep -o 'https://[^,[:space:]]*' | sort -u)

# 平铺前提：basename 不得冲突（2026-10-24 闸门实测全集无冲突；上游若引入冲突则停）
dups=$(printf '%s\n' "$urls" | sed 's|.*/||' | sort | uniq -d)
if [ -n "$dups" ]; then
  echo "ERROR: 规则集 basename 冲突，无法平铺：$dups" >&2
  exit 1
fi

echo "== 拉取规则集 $(printf '%s\n' "$urls" | wc -l | tr -d ' ') 个 =="
for u in $urls; do
  curl -fsSL --retry 3 -o "$(basename "$u")" "$u"
done

pulled_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=$(gh api "repos/${REPO}/commits/${BRANCH}" --jq .sha)

echo "== 写 SNAPSHOT_META (upstream ${commit}) =="
{
  printf '{\n'
  printf '  "pulledAt": "%s",\n' "$pulled_at"
  printf '  "upstream": {"repo": "%s", "branch": "%s", "commit": "%s"},\n' "$REPO" "$BRANCH" "$commit"
  printf '  "files": {\n'
  first=1
  for f in $(ls *.ini *.list | sort); do
    if [ "$first" -eq 1 ]; then first=0; else printf ',\n'; fi
    printf '    "%s": "%s"' "$f" "$($SHACMD "$f" | cut -d' ' -f1)"
  done
  printf '\n  }\n}\n'
} > SNAPSHOT_META

echo "done: $(ls *.ini | wc -l | tr -d ' ') ini + $(ls *.list | wc -l | tr -d ' ') list"
