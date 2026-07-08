package main

import (
	"net/http"
	"testing"
)

// clientIP 必須在源頭用 net.ParseIP 確認 IP 合法：非法值（含 CR/LF、標頭注入
// 字元）不得原樣流出，合法 IP 需正規化。CWE-640。
func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		trustProxy bool
		remote     string
		xff        string
		want       string
	}{
		{"ipv4 with port", false, "1.2.3.4:5678", "", "1.2.3.4"},
		{"ipv6 with port", false, "[::1]:5678", "", "::1"},
		{"xff not trusted", false, "1.2.3.4:5678", "9.9.9.9", "1.2.3.4"},
		{"xff trusted", true, "1.2.3.4:5678", "9.9.9.9", "9.9.9.9"},
		{"xff crlf injection", true, "1.2.3.4:5678", "9.9.9.9\r\nBcc: v@x.com", "unknown"},
		{"garbage remote", false, "not-an-ip", "", "unknown"},
	}
	s := &server{config: &Config{}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.config.TrustProxy = c.trustProxy
			r := &http.Request{RemoteAddr: c.remote, Header: http.Header{}}
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := s.clientIP(r); got != c.want {
				t.Fatalf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
}

// 確認 X-Forwarded-For 之類的髒值被代入信件時，CR/LF 與控制字元被移除，
// 無法注入額外標頭／收件人（CWE-640）。
func TestNotifyExpandStripsCRLF(t *testing.T) {
	vars := map[string]string{"ip": "1.2.3.4\r\nBcc: victim@x.com"}
	got := notifyExpand("client: {ip}", vars)
	want := "client: 1.2.3.4Bcc: victim@x.com"
	if got != want {
		t.Fatalf("CRLF not stripped: got %q want %q", got, want)
	}
}
