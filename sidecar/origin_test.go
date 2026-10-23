package main

import "testing"

func TestDeriveOrigin(t *testing.T) {
	tests := []struct {
		name           string
		publicBaseURL  string
		host           string
		forwardedProto string
		want           string
	}{
		{"PUBLIC_BASE_URL 覆盖优先且去尾斜杠", "https://sub.example.com/", "ignored", "ignored", "https://sub.example.com"},
		{"nginx 透传 Host 与 X-Forwarded-Proto", "", "192.168.1.10:8080", "http", "http://192.168.1.10:8080"},
		{"缺 X-Forwarded-Proto 时缺省 http", "", "127.0.0.1:18080", "", "http://127.0.0.1:18080"},
		{"https 反代场景", "", "s.example.com", "https", "https://s.example.com"},
	}
	for _, tt := range tests {
		if got := deriveOrigin(tt.publicBaseURL, tt.host, tt.forwardedProto); got != tt.want {
			t.Errorf("%s: deriveOrigin(%q, %q, %q) = %q, want %q",
				tt.name, tt.publicBaseURL, tt.host, tt.forwardedProto, got, tt.want)
		}
	}
}
