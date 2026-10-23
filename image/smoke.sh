#!/bin/sh
# 三步冒烟（发布门禁，make smoke-local 与 CI 共用），整个流程不依赖外网：
#   第一步  基础可用：/ 200、/version 200、/api/health 200 且 JSON 结构正确（含 PRESET_URL 覆盖生效断言）
#   第二步  fixture 转换：本地 python http.server 提供 fixture.ini + 内联 ss 节点，
#           经 /sub 以 Clash UA 与 Surge UA 直接转换，断言输出格式
#   第三步  端到端：POST /api/preview 取返回 link → 分别以 Clash/Surge UA 经 nginx 入口拉取该 link，
#           断言格式同上——证明「生成的链接可用」而不只是「/sub 能转」
# fixture 全本地（订阅节点为 RFC 5737 测试地址，无需真实出网）
# 用法: smoke.sh <image> [host-port]（默认 18080）
set -eu

IMAGE="${1:?用法: smoke.sh <image> [host-port]}"
PORT="${2:-18080}"
NAME=ssc-smoke
FIXTURES="$(cd "$(dirname "$0")/smoke-fixtures" && pwd)"
BASE="http://127.0.0.1:${PORT}"
SUB_NODE='ss://YWVzLTI1Ni1nY206dGVzdHBhc3N3b3Jk@192.0.2.1:8388#fixture'
CLASH_UA='clash-verge/v1.7.7'
SURGE_UA='Surge Mac/2398'

rc=0

# ---- fixture server：python3 静态服务（绑 0.0.0.0——host.docker.internal 进来的
# ---- 流量目标 IP 是宿主网关地址，绑 127.0.0.1 会拒收）
FIXTURE_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
python3 -m http.server "$FIXTURE_PORT" --bind 0.0.0.0 --directory "$FIXTURES" >/dev/null 2>&1 &
FIXTURE_PID=$!
CFG="http://host.docker.internal:${FIXTURE_PORT}/fixture.ini"
SUB_URL="http://host.docker.internal:${FIXTURE_PORT}/fixture-sub.txt"

# 跨平台：macOS（Docker Desktop / colima）原生解析 host.docker.internal；
# CI Linux 必须显式映射到 host-gateway
ADD_HOST_ARG=""
if [ "$(uname -s)" = "Linux" ]; then
    ADD_HOST_ARG="--add-host=host.docker.internal:host-gateway"
fi

cleanup() {
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    kill "$FIXTURE_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# ---- 起被测容器（PRESET_URL 指向本地 fixture，验证预设覆盖链路）
docker rm -f "$NAME" >/dev/null 2>&1 || true
# shellcheck disable=SC2086
docker run -d --name "$NAME" -p "127.0.0.1:${PORT}:80" \
    -e "PRESET_URL=${CFG}" \
    $ADD_HOST_ARG \
    "$IMAGE" >/dev/null

# 等待 /healthz 就绪（Extended 初始化 pref + nginx 起监听）
i=0
while [ "$i" -lt 30 ]; do
    code=$(curl -s -m 2 -o /dev/null -w '%{http_code}' "$BASE/healthz" || true)
    [ "$code" = "200" ] && break
    i=$((i + 1))
    sleep 1
done
if [ "$i" -ge 30 ]; then
    echo "FAIL /healthz 30s 内未就绪"
    exit 1
fi
echo "PASS 容器就绪（/healthz ${i}s）"

# ============ 第一步：基础可用 ============
assert_code() {
    got=$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$BASE$2" || echo 000)
    if [ "$got" = "$3" ]; then
        echo "PASS $1 ($2 -> $got)"
    else
        echo "FAIL $1 ($2 -> $got，期望 $3)"
        rc=1
    fi
}

assert_code "首页（前端静态）" / 200
assert_code "Extended /version（经 nginx）" /version 200

# /api/health：JSON 可解析 + 真实自采生效：
#   backendVersion 非 unknown（HTML 解析成功）；PRESET_URL 覆盖生效（url = fixture、lastModifiedAt
#   非 null（python http.server 必带 Last-Modified）→ status = fresh（文件 mtime = git checkout 时刻）
# sidecar 启动自采可能早于 Extended 就绪（未就绪时 5s 重试），轮询等全面就绪（上限 30s）
if python3 - "$BASE" <<'EOF'
import json, sys, time, urllib.request
base = sys.argv[1]
for _ in range(60):
    try:
        d = json.load(urllib.request.urlopen(base + "/api/health", timeout=5))
    except Exception:
        time.sleep(0.5); continue
    p = (d.get("presets") or [{}])[0]
    if d.get("backendVersion") != "unknown" and p.get("lastSuccessAt"):
        assert d["presets"][0]["status"] == "fresh", f"status={p['status']}"
        assert p["lastModifiedAt"], f"lastModifiedAt={p['lastModifiedAt']}"
        assert "host.docker.internal" in p["url"], f"preset url={p['url']}"
        sys.exit(0)
    time.sleep(0.5)
sys.exit("30s 内自采未就绪（backendVersion 或 lastSuccessAt 缺失）")
EOF
then
    echo "PASS /api/health 真实自采（backendVersion 解析 + PRESET_URL 覆盖 → fresh）"
else
    echo "FAIL /api/health 真实自采"
    rc=1
fi

if curl -s -m 5 "$BASE/api/clients" | python3 -c '
import json, sys
d = json.load(sys.stdin)
n = len(d["clients"])
assert n == 9, f"clients={n}"
assert any("host.docker.internal" in p["url"] for p in d["presets"]), "clients presets 未带 PRESET_URL 覆盖"
'; then
    echo "PASS /api/clients 200 且含 9 个 clients + 预设覆盖"
else
    echo "FAIL /api/clients"
    rc=1
fi

# ============ 第二步：fixture 转换（/sub 直转，双 UA） ============
assert_sub_output() {
    # $1 描述 $2 UA $3 target $4 断言子串
    body=$(curl -s -m 30 -G "$BASE/sub" \
        --data-urlencode "target=$3" \
        --data-urlencode "url=$SUB_NODE" \
        --data-urlencode "config=$CFG" \
        -A "$2" || true)
    case "$body" in
        *"$4"*)
            echo "PASS $1（输出含 $4）"
            ;;
        *)
            echo "FAIL $1（输出未含 $4）"
            rc=1
            ;;
    esac
}

assert_sub_output "Clash 直转（clash UA）" "$CLASH_UA" clash "proxies:"
assert_sub_output "Surge 直转（surge UA）" "$SURGE_UA" surge "[General]"

# ============ 第三步：端到端（/api/preview 取 link → 经 nginx 双 UA 拉取） ============
# 订阅 url 用 http fixture 链接（过 D7 预校验；容器内 Extended 直接拉本地 fixture）
LINK=$(curl -s -m 30 -X POST "$BASE/api/preview" \
    -H 'Content-Type: application/json' \
    -d "{\"url\":\"$SUB_URL\",\"target\":\"auto\"}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
assert d['ok'] is True, 'ok=%s code=%s msg=%s' % (d['ok'], d['errorCode'], d['message'])
assert d['errorCode'] is None, 'errorCode=%s' % d['errorCode']
assert d['target'] == 'clash', 'target=%s' % d['target']
assert d['link'].startswith(sys.argv[1] + '/sub?'), 'link=%s' % d['link']
print(d['link'])
" "$BASE") || LINK=""
if [ -n "$LINK" ]; then
    echo "PASS /api/preview ok:true（auto→clash 探测通过，link 已生成）"
else
    echo "FAIL /api/preview 未通过或 link 未生成"
    rc=1
fi

assert_fetch_link() {
    # $1 描述 $2 UA $3 断言子串
    if [ -z "$LINK" ]; then
        echo "SKIP $1（无 link）"
        return
    fi
    body=$(curl -s -m 30 -A "$2" "$LINK" || true)
    case "$body" in
        *"$3"*)
            echo "PASS $1（输出含 $3）"
            ;;
        *)
            echo "FAIL $1（输出未含 $3）"
            rc=1
            ;;
    esac
}

assert_fetch_link "经 nginx 以 clash UA 拉取 link" "$CLASH_UA" "proxies:"
assert_fetch_link "经 nginx 以 surge UA 拉取 link（auto 按 UA 适配）" "$SURGE_UA" "[General]"

if [ "$rc" -eq 0 ]; then
    echo "== 冒烟三步全部 PASS =="
fi
exit "$rc"
