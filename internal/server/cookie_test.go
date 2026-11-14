package server

import "testing"

func TestExtractCookie(t *testing.T) {
	plain := "ssoid=abc123; token=xyz; foo=bar"
	cases := []struct{ name, in, want string }{
		{"整串 cookie", plain, plain},
		{"带前缀", "Cookie: " + plain, plain},
		{"curl 单引号", "curl 'https://longcat.chat/api/v1/chat' \\\n  -H 'cookie: " + plain + "' \\\n  --data '{}'", plain},
		{"curl 双引号", `curl "https://longcat.chat/x" -H "cookie: ` + plain + `"`, plain},
		{"curl --cookie", "curl https://longcat.chat/x --cookie '" + plain + "'", plain},
	}
	for _, c := range cases {
		got, err := extractCookie(c.in)
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: 得到 %q 期望 %q", c.name, got, c.want)
		}
	}
	// 非法输入要报错
	for _, bad := range []string{"", "just-a-word", "curl 'https://x' -H 'accept: */*'"} {
		if _, err := extractCookie(bad); err == nil {
			t.Errorf("输入 %q 应当报错", bad)
		}
	}
}
