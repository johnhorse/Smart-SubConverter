#!/bin/sh
# PUBLIC_BASE_URL → MANAGED_CONFIG_PREFIX 联动（assignment-findings ⑤）
# Surge 系输出的 MANAGED-CONFIG 头默认回指 127.0.0.1:25500，朋友导入后更新链路断裂；
# Extended 原生支持 MANAGED_CONFIG_PREFIX env 覆盖。
# oneshot 进程环境干净，容器 env 从 stage0 写入的 /run/s6/container_environment 读取；
# 结果写文件，由 subconverter/run 读取后 export（不 eval，防注入）
pbu=""
[ -f /run/s6/container_environment/PUBLIC_BASE_URL ] && pbu="$(cat /run/s6/container_environment/PUBLIC_BASE_URL)"
mcp=""
[ -f /run/s6/container_environment/MANAGED_CONFIG_PREFIX ] && mcp="$(cat /run/s6/container_environment/MANAGED_CONFIG_PREFIX)"
if [ -n "$pbu" ] && [ -z "$mcp" ]; then
    printf '%s' "$pbu" > /run/ssc-managed-config-prefix
    echo "linked PUBLIC_BASE_URL -> MANAGED_CONFIG_PREFIX"
fi
