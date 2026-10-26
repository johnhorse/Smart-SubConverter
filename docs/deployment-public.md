# 公网部署（非官方）

V1 架构与验收只覆盖局域网。以下路径**不在 V1 验收范围内**，供有公网 HTTPS 需求的部署者参考。

公网档的三层防护：

1. **token 校验加在反代层（Caddy），scope 收窄到 `/sub*`**。实测结论：SubConverter-Extended 无原生 /sub 访问控制（配置与安全文档均无鉴权项），校验只能做在这层。v1.2.0 起语义变化：`SUB_TOKEN` 不再注入朋友拿到的链接（短链不带 token——短码即逐链接凭据，泄露走单条撤销）；它的用途是 sidecar 容器内调 `/sub` 时自动带上（朋友无感知）+ 管理端点（`GET /api/links` / `DELETE`）的 `?token=` 鉴权。Caddy 校验相应只拦 `/sub*`（存量长链直通路由），放行 `/`（页面）、`/s/`（短链）、`/api/`（sidecar 自行鉴权）——原全局 matcher 会把不带 token 的短链与页面全部 403
2. **转换内核安全档位**：`SUBCONVERTER_SECURITY_PROFILE=public` 阻止内核抓取回环/私网地址字面量
3. **容器端口只绑回环**（`127.0.0.1:18080:80`），对外只暴露 Caddy 的 443

容器侧：

```bash
docker run -d --name smart-subconverter \
  -p 127.0.0.1:18080:80 \
  -v smart-sub-data:/base/data \
  -e PUBLIC_BASE_URL=https://sub.example.com \
  -e SUB_TOKEN=CHANGE_ME_TO_A_LONG_RANDOM_SECRET \
  -e SUBCONVERTER_SECURITY_PROFILE=public \
  johnhorse/smart-subconverter:latest
```

Caddy 侧（`Caddyfile`，自动 HTTPS 由 Caddy ACME 处理）：

```caddyfile
sub.example.com {
	# token 校验只拦 /sub*（遗留长链直通路由，query 必须带 token）。
	# 显式放行：/（页面与静态资源）、/s/（短链——短码即逐链接凭据，本身不带
	# token）、/api/（管理端点由 sidecar 用 SUB_TOKEN 自行鉴权）。
	# 原全局 matcher 会把不带 token 的短链与页面全部 403，故收窄。
	@notoken {
		path /sub*
		not query token=CHANGE_ME_TO_A_LONG_RANDOM_SECRET
	}
	respond @notoken "Forbidden" 403

	reverse_proxy 127.0.0.1:18080
}
```

说明：

- 两处 `CHANGE_ME_TO_A_LONG_RANDOM_SECRET` 必须同值（生成建议：`openssl rand -hex 32`）
- token 防两处：盲猜 `/sub` 直通 URL（存量长链滥用）与管理端点越权；朋友侧全程无感知（短链不带 token，容器内自动注入；管理端点是部署者自己用的）
- 上述 Caddyfile 的 matcher 写法（named matcher 内 `path` 与 `not query` 按 AND 组合）是 Caddy 标准语义，与 v1.1 已验证版本同构；上线前请照旧用 `caddy:2-alpine` 的 `caddy adapt` 自行验证，并确认无 token 时 `/sub*` 403、`/` `/s/` `/api/` 放行
