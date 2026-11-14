package paste

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
		got, err := ExtractCookie(c.in)
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
		if _, err := ExtractCookie(bad); err == nil {
			t.Errorf("输入 %q 应当报错", bad)
		}
	}
}

// 妙手 token 的各种粘贴形态
func TestExtractMiaoshouToken(t *testing.T) {
	tok := "AgGFIqTPBfWz1OCqDIlIOVxVmTY6BmGBWZePgadtLn1MBGnMSN4oxDXDo8xHUO45QzFVsTY1HRlI8g"
	cases := []struct{ name, in, want string }{
		{"裸 token", tok, tok},
		{"带 access-token 头", "access-token: " + tok, tok},
		{"X-Auth-Token 头", "X-Auth-Token: " + tok, tok},
		{"Bearer", "Authorization: Bearer " + tok, tok},
		{"curl", "curl 'https://nocode.cn/api/chat/create' \\\n  -H 'access-token: " + tok + "' \\\n  --data '{}'", tok},
		{"JSON", `{"access_token":"` + tok + `","modified_at":1793942833066}`, tok},
	}
	for _, c := range cases {
		got, err := ExtractMiaoshouToken(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: got=%q err=%v", c.name, got, err)
		}
	}
	for _, bad := range []string{"", "short", "has space inside here"} {
		if _, err := ExtractMiaoshouToken(bad); err == nil {
			t.Errorf("输入 %q 应报错", bad)
		}
	}
}
