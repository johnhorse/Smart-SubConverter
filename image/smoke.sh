#!/bin/sh
# 四步冒烟（发布门禁，make smoke-local 与 CI 共用），整个流程不依赖外网：
#   第一步  基础可用：/ 200、/version 200、/api/health 200 且 JSON 结构正确（含 PRESET_URL 覆盖生效断言）
#   第二步  fixture 转换：本地 python http.server 提供 fixture.ini + 内联 ss 节点，
#           经 /sub（deprecated 遗留直通路由）以 Clash UA 与 Surge UA 直接转换，断言输出格式
#   第三步  端到端（短链，v1.2.0）：POST /api/preview 取返回短链（断言 /s/ + 8 字符 base62 码）
#           → 分别以 Clash/Surge UA 经 nginx 入口拉取该短链，断言格式同上；
#           另断言：同参数二次 preview → link 相同（指纹幂等）、docker restart 后同一短链
#           仍可用（state.db 持久化）、管理端点 GET /api/links 含码 → DELETE 204 幂等 →
#           删后拉取 404（SUB_TOKEN 未配 → 端点开放）
#   第四步  快照本地化（v1.3.0）：快照 fixture 注入挂载的 DATA_DIR（不依赖镜像内
#           /base/rules-snapshot 出厂层）；断言 health source=snapshot、default 档走快照
#           路径输出 DOMAIN=94/GEOIP=1（闸门实测基线，静默丢规则防护）、存量短链
#           GitHub config URL → /s/ 运行时映射本地产物；数据源 image/rules-snapshot/
#           缺失时整段 SKIP（vendor 出厂层落地后自动生效）
# fixture 全本地（订阅节点为 RFC 5737 测试地址，无需真实出网）
# 用法: smoke.sh <image> [host-port]（默认 18080）
set -eu

IMAGE="${1:?用法: smoke.sh <image> [host-port]}"
PORT="${2:-18080}"
NAME=ssc-smoke
NAME2=ssc-smoke-snap
NAME3=ssc-smoke-map
FIXTURES="$(cd "$(dirname "$0")/smoke-fixtures" && pwd)"
BASE="http://127.0.0.1:${PORT}"
SUB_NODE='ss://YWVzLTI1Ni1nY206dGVzdHBhc3N3b3Jk@192.0.2.1:8388#fixture'
CLASH_UA='clash-verge/v1.7.7'
SURGE_UA='Surge Mac/2398'

rc=0

# ---- fixture server：python3 静态服务（绑 0.0.0.0——host.docker.internal 进来的
# ---- 流量目标 IP 是宿主网关地址，绑 127.0.0.1 会拒收）
# touch 刷新 mtime：豁免档 status 判定「Last-Modified < 24h → fresh」依赖文件 mtime，
# 仓库 checkout 时间跨 24h 后会静默变 stale（时间炸弹，2026-10-25 抓过一次）
touch "$FIXTURES"/*.ini "$FIXTURES"/*.txt 2>/dev/null || true
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
    docker rm -f "$NAME" "$NAME2" "$NAME3" >/dev/null 2>&1 || true
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

# 等待 /healthz 就绪（Extended 初始化 pref + nginx 起监听；重启后复用）
wait_healthz() {
    base="${1:-$BASE}"
    i=0
    while [ "$i" -lt 30 ]; do
        code=$(curl -s -m 2 -o /dev/null -w '%{http_code}' "$base/healthz" || true)
        [ "$code" = "200" ] && return 0
        i=$((i + 1))
        sleep 1
    done
    return 1
}
if wait_healthz; then
    echo "PASS 容器就绪（/healthz）"
else
    echo "FAIL /healthz 30s 内未就绪"
    exit 1
fi

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
ps = d["presets"]
assert len(ps) == 3, f"presets={len(ps)}"
ids = [p["id"] for p in ps]
assert ids == ["default", "full_multimode", "full_adblockplus"], f"preset ids={ids}"
u0 = ps[0]["url"]
assert "host.docker.internal" in u0, f"default 档未带 PRESET_URL 覆盖: {u0}"
n0 = ps[0]["name"]
assert n0 == "ACL4SSR 默认", f"default name={n0}"
u1 = ps[1]["url"]
u2 = ps[2]["url"]
assert u1 == "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini", f"multimode url={u1}"
assert u2 == "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_AdblockPlus.ini", f"adblockplus url={u2}"
'; then
    echo "PASS /api/clients 200：9 clients + 3 presets（default 档带 PRESET_URL 覆盖，其余两档内置）"
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
import json, re, sys
d = json.load(sys.stdin)
assert d['ok'] is True, 'ok=%s code=%s msg=%s' % (d['ok'], d['errorCode'], d['message'])
assert d['errorCode'] is None, 'errorCode=%s' % d['errorCode']
assert d['target'] == 'clash', 'target=%s' % d['target']
link = d['link']
assert link.startswith(sys.argv[1] + '/s/'), 'link=%s' % link
assert re.fullmatch('[0-9A-Za-z]{8}', link.rsplit('/', 1)[1]), '短码非 8 字符 base62: %s' % link
print(link)
" "$BASE") || LINK=""
CODE=${LINK##*/}
if [ -n "$LINK" ]; then
    echo "PASS /api/preview ok:true（auto→clash 探测通过，短链 /s/$CODE 已生成）"
else
    echo "FAIL /api/preview 未通过或短链未生成"
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

assert_fetch_link "经 nginx 以 clash UA 拉取短链" "$CLASH_UA" "proxies:"
assert_fetch_link "经 nginx 以 surge UA 拉取短链（auto 按 UA 适配）" "$SURGE_UA" "[General]"

# ---- 同参数幂等：同 url+target+preset 二次 preview → link 完全相同（指纹命中复用既有码）
if [ -n "$LINK" ]; then
    if curl -s -m 30 -X POST "$BASE/api/preview" \
        -H 'Content-Type: application/json' \
        -d "{\"url\":\"$SUB_URL\",\"target\":\"auto\"}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
assert d['link'] == sys.argv[1], 'link=%s 与首次 %s 不一致' % (d['link'], sys.argv[1])
" "$LINK"; then
        echo "PASS 同参数二次 preview → link 完全相同（指纹幂等）"
    else
        echo "FAIL 同参数二次 preview link 漂移"
        rc=1
    fi
else
    echo "SKIP 同参数幂等（无 link）"
fi

# ---- preset 档位链路：full_multimode → preset 回显 + link 为短链形态
# （v1.2.0 起短链不内嵌 config=，档位烙进服务端存储；PRESET_URL 只覆盖 default 档；
#   不断言 ok——离线环境 Extended 拉不到 GitHub config，探测失败属预期）
if curl -s -m 30 -X POST "$BASE/api/preview" \
    -H 'Content-Type: application/json' \
    -d "{\"url\":\"$SUB_URL\",\"target\":\"auto\",\"preset\":\"full_multimode\"}" | python3 -c "
import json, re, sys
d = json.load(sys.stdin)
assert d['preset'] == {'id': 'full_multimode', 'name': '地区分组 + AI 分流'}, 'preset=%s' % d['preset']
link = d['link']
assert link.startswith(sys.argv[1] + '/s/'), 'link=%s' % link
assert re.fullmatch('[0-9A-Za-z]{8}', link.rsplit('/', 1)[1]), '短码非 8 字符 base62: %s' % link
" "$BASE"; then
    echo "PASS /api/preview preset=full_multimode（preset 回显 + link 为短链）"
else
    echo "FAIL /api/preview preset=full_multimode"
    rc=1
fi

# ---- 导入深链：target=surge → deepLink 以 surge://install-config?url= 开头且 unescape 后等于
# ---- link；target=clash 与缺省（auto）→ null（契约「导入深链」节；不断言 ok——深链与探测结果无关）
if curl -s -m 30 -X POST "$BASE/api/preview" \
    -H 'Content-Type: application/json' \
    -d "{\"url\":\"$SUB_URL\",\"target\":\"surge\"}" | python3 -c "
import json, sys, urllib.parse
d = json.load(sys.stdin)
dl = d['deepLink']
assert dl is not None, 'deepLink 为 null'
assert dl.startswith('surge://install-config?url='), 'deepLink=%s' % dl
dec = urllib.parse.unquote_plus(dl[len('surge://install-config?url='):])
assert dec == d['link'], 'unescape(deepLink)=%s link=%s' % (dec, d['link'])
"; then
    echo "PASS /api/preview target=surge deepLink（surge 前缀 + unescape 后等于 link）"
else
    echo "FAIL /api/preview target=surge deepLink"
    rc=1
fi

assert_deep_link_null() {
    # $1 描述 $2 请求 JSON
    if curl -s -m 30 -X POST "$BASE/api/preview" \
        -H 'Content-Type: application/json' \
        -d "$2" | python3 -c '
import json, sys
d = json.load(sys.stdin)
assert d["deepLink"] is None, "deepLink=%s 应为 null" % d["deepLink"]
'; then
        echo "PASS $1（deepLink=null）"
    else
        echo "FAIL $1（deepLink 应为 null）"
        rc=1
    fi
}

assert_deep_link_null "/api/preview target=clash deepLink" "{\"url\":\"$SUB_URL\",\"target\":\"clash\"}"
assert_deep_link_null "/api/preview 缺省 target（auto）deepLink" "{\"url\":\"$SUB_URL\"}"

# ---- 重启持久化：docker restart 后同一短链仍可用（state.db 在容器 /data，随容器层存活）
if [ -n "$CODE" ]; then
    docker restart "$NAME" >/dev/null
    if wait_healthz; then
        body=$(curl -s -m 30 -A "$CLASH_UA" "$LINK" || true)
        case "$body" in
            *"proxies:"*)
                echo "PASS docker restart 后同一短链仍可用（state.db 持久化）"
                ;;
            *)
                echo "FAIL docker restart 后短链拉取异常"
                rc=1
                ;;
        esac
    else
        echo "FAIL docker restart 后 /healthz 30s 内未就绪"
        rc=1
    fi
else
    echo "SKIP 重启持久化（无短码）"
fi

# ---- 管理端点（SUB_TOKEN 未配 → 开放）：列表含本次码 → 删除幂等 204 → 删后拉取 404
if [ -n "$CODE" ]; then
    if curl -s -m 10 "$BASE/api/links" | python3 -c "
import json, sys
d = json.load(sys.stdin)
codes = [l['code'] for l in d['links']]
assert sys.argv[1] in codes, 'links 未含 %s: %s' % (sys.argv[1], codes)
" "$CODE"; then
        echo "PASS GET /api/links 200 且含本次短码（SUB_TOKEN 未配 → 开放）"
    else
        echo "FAIL GET /api/links 未含本次短码"
        rc=1
    fi

    n=1
    while [ "$n" -le 2 ]; do
        got=$(curl -s -m 10 -X DELETE -o /dev/null -w '%{http_code}' "$BASE/api/links/$CODE" || echo 000)
        if [ "$got" = "204" ]; then
            echo "PASS DELETE /api/links/{code} 第 $n 次 → 204（幂等）"
        else
            echo "FAIL DELETE /api/links/{code} 第 $n 次 → $got（期望 204）"
            rc=1
        fi
        n=$((n + 1))
    done

    assert_code "删码后拉取同一短链" "/s/$CODE" 404
else
    echo "SKIP 管理端点用例（无短码）"
fi

# ============ 第四步：快照本地化（v1.3.0） ============
# 快照 fixture 注入「挂载的 DATA_DIR」（-v hostdir:/base/data），不依赖镜像内
# /base/rules-snapshot 出厂层（开发期镜像可无 vendor）。数据源默认本仓库
# image/rules-snapshot/（vendor 入库产物），SNAPSHOT_SRC 可覆盖；缺失整段 SKIP
SNAPSHOT_SRC="${SNAPSHOT_SRC:-$(cd "$(dirname "$0")" && pwd)/rules-snapshot}"
if [ -f "$SNAPSHOT_SRC/ACL4SSR_Online.ini" ]; then
    MULTI_URL="https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/ACL4SSR_Online_Full_MultiMode.ini"

    # 容器2：不带 PRESET_URL（default 档本地化）+ 注入快照的 DATA_DIR
    SNAP_DIR=$(mktemp -d)
    mkdir -p "$SNAP_DIR/rules"
    cp "$SNAPSHOT_SRC"/*.ini "$SNAPSHOT_SRC"/*.list "$SNAP_DIR/rules/" 2>/dev/null || true
    docker rm -f "$NAME2" >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    docker run -d --name "$NAME2" -p "127.0.0.1:$((PORT+1)):80" \
        -v "$SNAP_DIR":/base/data \
        -e "PUBLIC_BASE_URL=http://127.0.0.1:$((PORT+1))" \
        $ADD_HOST_ARG \
        "$IMAGE" >/dev/null
    BASE2="http://127.0.0.1:$((PORT+1))"
    if wait_healthz "$BASE2"; then
        echo "PASS 快照容器就绪（/healthz）"
    else
        echo "FAIL 容器2 /healthz 30s 内未就绪"
        rc=1
    fi

    # health：default 档 source=snapshot（快照刷新循环驱动，不再远端 HEAD）
    if python3 - "$BASE2" <<'EOF'
import json, sys, time, urllib.request
base = sys.argv[1]
for _ in range(60):
    try:
        d = json.load(urllib.request.urlopen(base + "/api/health", timeout=5))
    except Exception:
        time.sleep(0.5)
        continue
    p = (d.get("presets") or [{}])[0]
    if p.get("source") == "snapshot":
        sys.exit(0)
    time.sleep(0.5)
sys.exit("30s 内 health 未出现 source=snapshot")
EOF
    then
        echo "PASS /api/health source=snapshot（default 档快照本地化生效）"
    else
        echo "FAIL /api/health source=snapshot"
        rc=1
    fi

    # default 档走快照路径出完整配置：preview → /s/ 短链拉取 → rules 数 == GitHub 基线
    # （DOMAIN 94 / GEOIP 1，闸门实测值——改写丢规则时此处数字缩水即抓出）
    LINK2=$(curl -s -m 60 -X POST "$BASE2/api/preview" \
        -H 'Content-Type: application/json' \
        -d "{\"url\":\"$SUB_URL\",\"target\":\"clash\"}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
assert d['ok'] is True, 'ok=%s code=%s msg=%s' % (d['ok'], d['errorCode'], d['message'])
assert '/s/' in d['link'], 'link=%s' % d['link']
print(d['link'])
") || LINK2=""
    if [ -n "$LINK2" ]; then
        if curl -s -m 60 -A "$CLASH_UA" "$LINK2" | python3 -c '
import re, sys
text = sys.stdin.read()
in_rules = False
dom = geo = 0
for line in text.splitlines():
    if re.match(r"^rules:\s*$", line):
        in_rules = True
        continue
    if not in_rules:
        continue
    m = re.match(r"^\s*-\s+([A-Z0-9-]+),", line)
    if not m:
        if line and not line[0].isspace():
            break
        continue
    k = m.group(1)
    if k.startswith("DOMAIN"):
        dom += 1
    elif k.startswith("GEOIP"):
        geo += 1
assert "proxies:" in text, "输出无 proxies:"
assert dom == 94, "DOMAIN 行 = %d, want 94（静默丢规则防护基线）" % dom
assert geo == 1, "GEOIP 行 = %d, want 1" % geo
'; then
            echo "PASS 本地化档经 /s/ 输出 DOMAIN=94 GEOIP=1（== GitHub 基线）"
        else
            echo "FAIL 本地化档输出规则数 != GitHub 基线（DOMAIN 94 / GEOIP 1）"
            rc=1
        fi
    else
        echo "FAIL 快照容器 /api/preview 未通过（ok 应为 true——引擎读本地快照不出网）"
        rc=1
    fi

    # v1.3.1 QX 链路（双跳）：① quanx UA 输出的 filter_remote 引用 getruleset 且前缀 =
    # PUBLIC_BASE_URL（非 127.0.0.1）；② 该 getruleset URL 经 nginx 真实拉到规则内容（非 HTML）
    LINKQX=$(curl -s -m 60 -X POST "$BASE2/api/preview" \
        -H 'Content-Type: application/json' \
        -d "{\"url\":\"$SUB_URL\",\"target\":\"auto\"}" | python3 -c "
import json, sys
print(json.load(sys.stdin)['link'])
") || LINKQX=""
    if [ -n "$LINKQX" ]; then
        QX_OUT=$(curl -s -m 60 -A "Quantumult%20X%20v1.0.30" "$LINKQX" || true)
        RULE_URL=$(printf '%s' "$QX_OUT" | grep -m 1 "getruleset" | grep -o "http[^,]*" | head -1)
        if printf '%s' "$QX_OUT" | grep -q "^\[general\|^\[server_remote\|\[filter_remote\]" \
            && [ -n "$RULE_URL" ] \
            && case "$RULE_URL" in "http://127.0.0.1:$((PORT+1))"*) true;; *) false;; esac; then
            echo "PASS QX 输出 getruleset 前缀 == PUBLIC_BASE_URL（不回指 127.0.0.1:25500）"
        else
            echo "FAIL QX 输出应含 filter_remote 且 getruleset 前缀为 PUBLIC_BASE_URL：$RULE_URL"
            rc=1
        fi
        # v1.3.3 两段式：QX 输出应为 [server_local] 明文节点（服务端代取），不再让 QX 自拉机场
        if printf '%s' "$QX_OUT" | grep -q "\[server_local\]" \
            && printf '%s' "$QX_OUT" | grep -q "^shadowsocks = \| vmess = "; then
            echo "PASS QX 两段式 server_local 明文节点（服务端代取）"
        else
            echo "FAIL QX 输出应含 [server_local] 明文节点：$(printf '%s' "$QX_OUT" | head -c 120)"
            rc=1
        fi
        if [ -n "$RULE_URL" ]; then
            RULE_BODY=$(curl -s -m 30 "$RULE_URL" || true)
            case "$RULE_BODY" in
                *"<html"*|*"<!"*)
                    echo "FAIL getruleset 经 nginx 返回 HTML（路由缺失，落入前端静态）"
                    rc=1
                    ;;
                "")
                    echo "FAIL getruleset 返回空"
                    rc=1
                    ;;
                *)
                    echo "PASS getruleset 经 nginx 拉到规则内容（QX 双跳闭环）"
                    ;;
            esac
        fi
    else
        echo "FAIL 容器2 /api/preview（auto）未生成 link"
        rc=1
    fi

    # 容器3：PRESET_URL=multimode GitHub URL（default 档豁免、建链冻结该内置 URL）
    # → /s/ 运行时映射本地改写产物：离线仍出完整配置（存量短链原地升级）
    SNAP_DIR2=$(mktemp -d)
    mkdir -p "$SNAP_DIR2/rules"
    cp "$SNAPSHOT_SRC"/*.ini "$SNAPSHOT_SRC"/*.list "$SNAP_DIR2/rules/" 2>/dev/null || true
    docker rm -f "$NAME3" >/dev/null 2>&1 || true
    # shellcheck disable=SC2086
    docker run -d --name "$NAME3" -p "127.0.0.1:$((PORT+2)):80" \
        -v "$SNAP_DIR2":/base/data \
        -e "PRESET_URL=$MULTI_URL" \
        $ADD_HOST_ARG \
        "$IMAGE" >/dev/null
    BASE3="http://127.0.0.1:$((PORT+2))"
    if wait_healthz "$BASE3"; then
        LINK3=$(curl -s -m 60 -X POST "$BASE3/api/preview" \
            -H 'Content-Type: application/json' \
            -d "{\"url\":\"$SUB_URL\",\"target\":\"clash\"}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
# 豁免档探测 config=GitHub URL，离线探测失败属预期（ok:false 不拦 link 生成——D4）
print(d['link'])
") || LINK3=""
        if [ -n "$LINK3" ]; then
            body=$(curl -s -m 60 -A "$CLASH_UA" "$LINK3" || true)
            case "$body" in
                *"proxies:"*)
                    echo "PASS 存量短链 GitHub config URL → /s/ 运行时映射本地产物（离线仍 200）"
                    ;;
                *)
                    echo "FAIL 存量短链映射后拉取异常（应映射本地改写 .ini 出完整配置）"
                    rc=1
                    ;;
            esac
            # v1.3.1 错误内容协商：浏览器直开（Accept html + 引擎不识的 UA）→ 人话 HTML 而非冷 JSON。
            # 必须用 auto 链（target=explicit 时引擎不做 UA 识别，任何 UA 都 200）
            LINK4=$(curl -s -m 60 -X POST "$BASE3/api/preview" \
                -H 'Content-Type: application/json' \
                -d "{\"url\":\"$SUB_URL\"}" | python3 -c "
import json, sys
print(json.load(sys.stdin)['link'])
") || LINK4=""
            if [ -n "$LINK4" ]; then
                html_body=$(curl -s -m 30 -H "Accept: text/html" -A "Mozilla/5.0" "$LINK4" || true)
                case "$html_body" in
                    *"订阅链接"*"客户端"*)
                        echo "PASS 浏览器直开 /s/ → 人话 HTML（v1.3.1 内容协商）"
                        ;;
                    *)
                        echo "FAIL 浏览器直开 /s/ 应返回人话 HTML：$html_body"
                        rc=1
                        ;;
                esac
                json_body=$(curl -s -m 30 -A "Mozilla/5.0" "$LINK4" || true)
                case "$json_body" in
                    *"upstream_unavailable"*)
                        echo "PASS 无 Accept 直开 /s/ → JSON error code 不变"
                        ;;
                    *)
                        echo "FAIL 无 Accept 应维持 JSON 错误码：$json_body"
                        rc=1
                        ;;
                esac
            else
                echo "FAIL 容器3 /api/preview（auto）未生成 link"
                rc=1
            fi
        else
            echo "FAIL 容器3 /api/preview 未生成 link"
            rc=1
        fi

        # v1.3.4 SUBSCRIPTION_PROXY：env 非空 → s6 启动改写 pref proxy_subscription（默认 NONE）
        NAME4="ssc-smoke-subproxy"
        docker rm -f "$NAME4" >/dev/null 2>&1 || true
        docker run -d --name "$NAME4" -e SUBSCRIPTION_PROXY=http://host.docker.internal:7890 "$IMAGE" >/dev/null
        sleep 5
        if docker exec "$NAME4" grep -q '^proxy_subscription = "http://host.docker.internal:7890"' /base/pref.toml 2>/dev/null; then
            echo "PASS SUBSCRIPTION_PROXY 改写 pref proxy_subscription 生效"
        else
            echo "FAIL SUBSCRIPTION_PROXY 未改写 /base/pref.toml"
            rc=1
        fi
        docker rm -f "$NAME4" >/dev/null 2>&1 || true
    else
        echo "FAIL 容器3 /healthz 30s 内未就绪"
        rc=1
    fi
else
    echo "SKIP 第四步快照本地化（$SNAPSHOT_SRC 缺 ACL4SSR_Online.ini——vendor 出厂层未落地，落地后自动生效）"
fi

if [ "$rc" -eq 0 ]; then
    echo "== 冒烟四步全部 PASS =="
fi
exit "$rc"
