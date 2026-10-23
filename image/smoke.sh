#!/bin/sh
# 冒烟 v0：镜像起容器后的最小验收（CI 与 make smoke-local 共用）
# T2 将扩展为三步：fixture 转换 + /api/preview 返回的 link 双 UA 拉取断言
set -eu

BASE="${1:?用法: smoke.sh <base-url，如 http://127.0.0.1:18080>}"

# 等待 /healthz 就绪（Extended 初始化 pref + nginx 起监听）
i=0
while [ "$i" -lt 30 ]; do
    code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/healthz" || true)
    [ "$code" = "200" ] && break
    i=$((i + 1))
    sleep 1
done
if [ "$i" -ge 30 ]; then
    echo "FAIL /healthz 30s 内未就绪"
    exit 1
fi
echo "PASS /healthz 就绪（${i}s）"

rc=0
assert_code() {
    got=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$2" || echo 000)
    if [ "$got" = "$3" ]; then
        echo "PASS $1 ($2 -> $got)"
    else
        echo "FAIL $1 ($2 -> $got，期望 $3)"
        rc=1
    fi
}

assert_code "首页（前端静态）" / 200
assert_code "Extended /version（经 nginx）" /version 200

# /api/health：JSON 可解析且结构正确（桩也应符合契约形状）
if curl -s "$BASE/api/health" | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert "backendVersion" in d and isinstance(d["presets"], list)
assert d["presets"] and all("status" in p for p in d["presets"])
'; then
    echo "PASS /api/health 200 且 JSON 可解析"
else
    echo "FAIL /api/health"
    rc=1
fi

# /api/clients：恰好 9 个 client（契约注册表）
if curl -s "$BASE/api/clients" | python3 -c '
import json, sys
d = json.load(sys.stdin)
n = len(d["clients"])
assert n == 9, f"clients={n}"
'; then
    echo "PASS /api/clients 200 且含 9 个 clients"
else
    echo "FAIL /api/clients"
    rc=1
fi

exit "$rc"
