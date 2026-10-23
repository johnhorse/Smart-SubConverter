package main

import "strings"

// deriveOrigin 推导 /api/preview 生成链接的 origin（契约 R3-1 定案，优先级从高到低）：
//  1. env PUBLIC_BASE_URL（去尾部 /）
//  2. X-Forwarded-Proto 头（缺失按 http）+ Host 头
//     （nginx 反代必须显式透传两者，否则 sidecar 只能看到内部地址）
//
// Wave 2 在 handlePreview 中接入本函数。
func deriveOrigin(publicBaseURL, host, forwardedProto string) string {
	if publicBaseURL != "" {
		return strings.TrimSuffix(publicBaseURL, "/")
	}
	proto := forwardedProto
	if proto == "" {
		proto = "http"
	}
	return proto + "://" + host
}
