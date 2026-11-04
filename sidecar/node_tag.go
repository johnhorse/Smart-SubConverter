package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// tagNodeURI 给单个节点 URI 增加 [tag] 前缀（Fail-Open 守则：任何解析异常均返回原 uri）
func tagNodeURI(uri, tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || uri == "" {
		return uri
	}

	i := strings.Index(uri, "://")
	if i <= 0 {
		return uri
	}
	scheme := strings.ToLower(uri[:i])

	if scheme == "vmess" {
		return tagVmessURI(uri, tag)
	}

	return tagFragmentURI(uri, tag)
}

// tagVmessURI 改写 vmess://<base64> 中的 ps 字段
func tagVmessURI(uri, tag string) string {
	payload := uri[8:] // skip "vmess://"
	hashSuffix := ""
	if j := strings.IndexByte(payload, '#'); j >= 0 {
		hashSuffix = payload[j:]
		payload = payload[:j]
	}

	b, err := decodeVmessB64(payload)
	if err != nil {
		return uri
	}

	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return uri
	}

	ps, _ := m["ps"].(string)
	m["ps"] = fmt.Sprintf("[%s] %s", tag, ps)

	newJSON, err := json.Marshal(m)
	if err != nil {
		return uri
	}

	return "vmess://" + base64.StdEncoding.EncodeToString(newJSON) + hashSuffix
}

// tagFragmentURI 改写带 # 的节点 URI（ss, ssr, trojan, vless, hysteria2 等）
func tagFragmentURI(uri, tag string) string {
	hashIdx := strings.IndexByte(uri, '#')
	if hashIdx < 0 {
		return uri
	}

	basePart := uri[:hashIdx]
	rawFrag := uri[hashIdx+1:]
	if rawFrag == "" {
		return uri
	}

	// 尝试 QueryUnescape 解码已存在的 URL 编码名称
	name := rawFrag
	if unescaped, err := url.QueryUnescape(rawFrag); err == nil && unescaped != "" {
		name = unescaped
	}

	newName := fmt.Sprintf("[%s] %s", tag, name)
	// 使用 %20 代替 +，保持大多数客户端的友好展示
	escapedNew := strings.ReplaceAll(url.QueryEscape(newName), "+", "%20")

	return basePart + "#" + escapedNew
}

// tagNodeURIs 批量为 URI 列表加前缀
func tagNodeURIs(uris []string, tag string) []string {
	if strings.TrimSpace(tag) == "" {
		return uris
	}
	out := make([]string, len(uris))
	for idx, u := range uris {
		out[idx] = tagNodeURI(u, tag)
	}
	return out
}
