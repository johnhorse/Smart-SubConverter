package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestTagNodeURI_Fragment(t *testing.T) {
	// ss
	rawSS := "ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.2.3.4:8388#HongKong%2001"
	taggedSS := tagNodeURI(rawSS, "主力")
	if !strings.Contains(taggedSS, "#%5B%E4%B8%BB%E5%8A%9B%5D%20HongKong%2001") {
		t.Fatalf("unexpected tagged ss: %s", taggedSS)
	}

	// vless 带未转义汉字
	rawVless := "vless://uuid@1.2.3.4:443?type=tcp&security=tls#香港节点-02"
	taggedVless := tagNodeURI(rawVless, "备用")
	if !strings.HasPrefix(taggedVless, "vless://uuid@1.2.3.4:443?type=tcp&security=tls#") {
		t.Fatalf("vless base part altered: %s", taggedVless)
	}

	// 无 #
	noHash := "trojan://pass@1.2.3.4:443"
	if tagNodeURI(noHash, "主力") != noHash {
		t.Fatalf("should not alter uri without hash")
	}

	// 空 tag
	if tagNodeURI(rawSS, "") != rawSS {
		t.Fatalf("should not alter uri with empty tag")
	}
}

func TestTagNodeURI_Vmess(t *testing.T) {
	vmessObj := map[string]interface{}{
		"v":    "2",
		"ps":   "美国节点-01",
		"add":  "1.2.3.4",
		"port": 443,
		"id":   "abcd-1234",
		"aid":  0,
		"net":  "ws",
		"type": "none",
		"host": "us.example.com",
		"path": "/ws",
		"tls":  "tls",
	}
	b, _ := json.Marshal(vmessObj)
	rawURI := "vmess://" + base64.StdEncoding.EncodeToString(b)

	tagged := tagNodeURI(rawURI, "主力")
	if !strings.HasPrefix(tagged, "vmess://") {
		t.Fatalf("expected vmess:// prefix: %s", tagged)
	}

	// 解码验证 ps 包含 [主力]
	b64Payload := tagged[8:]
	decoded, err := base64.StdEncoding.DecodeString(b64Payload)
	if err != nil {
		t.Fatalf("decode vmess failed: %v", err)
	}
	var res map[string]interface{}
	if err := json.Unmarshal(decoded, &res); err != nil {
		t.Fatalf("unmarshal vmess json failed: %v", err)
	}
	if res["ps"] != "[主力] 美国节点-01" {
		t.Fatalf("expected ps to be '[主力] 美国节点-01', got: %v", res["ps"])
	}
}

func TestTagNodeURI_FailOpen(t *testing.T) {
	// 损坏的 vmess base64
	badVmess := "vmess://not-valid-base64!@#$"
	if tagNodeURI(badVmess, "主力") != badVmess {
		t.Fatalf("expected fail open on invalid vmess")
	}

	// 空字符串
	if tagNodeURI("", "主力") != "" {
		t.Fatalf("expected empty output for empty input")
	}
}
