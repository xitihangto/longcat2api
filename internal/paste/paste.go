// Package paste 解析用户从浏览器 / DevTools 里粘贴进来的凭据。
//
// 支持的形态：裸 token、HTTP 头片段（access-token / X-Auth-Token / Bearer）、
// 「Copy as cURL」整段命令、含 access_token 的 JSON、以及 Cookie 整串。
package paste

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ExtractMiaoshouToken 从各种粘贴形态里取出妙手 access token。
func ExtractMiaoshouToken(input string) (string, error) {
	in := strings.TrimSpace(input)
	low := strings.ToLower(in)

	// cURL / 请求头形态：优先找明确的鉴权头
	for _, re := range []string{
		`(?i)(?:-H|--header)\s+['"]?\s*(?:access-token|x-auth-token|x-passport-token)\s*:\s*([^'"\s]+)`,
		`(?i)['"]?\s*(?:access-token|x-auth-token|x-passport-token)\s*:\s*([A-Za-z0-9\-_.=+/]{20,})`,
		`(?i)authorization\s*:\s*bearer\s+([A-Za-z0-9\-_.=+/]{20,})`,
	} {
		if m := regexp.MustCompile(re).FindStringSubmatch(in); len(m) > 1 {
			return strings.TrimSpace(m[1]), nil
		}
	}
	// JSON 形态
	if strings.Contains(low, "access_token") {
		var j struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal([]byte(in), &j); err == nil && j.AccessToken != "" {
			return j.AccessToken, nil
		}
		if m := regexp.MustCompile(`"access_token"\s*:\s*"([^"]+)"`).FindStringSubmatch(in); len(m) > 1 {
			return m[1], nil
		}
	}
	// 裸 token：妙手的是 AgGF 开头的一长串
	if strings.Contains(in, " ") || strings.Contains(in, "\n") {
		return "", fmt.Errorf("这不像 token（含空格或换行）。请粘裸 token，或带 access-token 头的 cURL")
	}
	if len(in) < 20 {
		return "", fmt.Errorf("token 太短（%d 字符），妙手 token 通常 100 字符以上", len(in))
	}
	return in, nil
}

// ExtractCookie 支持三种粘贴形态：
//  1. 直接一整串 Cookie（"a=1; b=2"）
//  2. 浏览器 DevTools「Copy as cURL」的整段命令（从中抠 -H 'cookie: ...'）
//  3. 只粘了若干 "name=value" 片段
func ExtractCookie(input string) (string, error) {
	in := strings.TrimSpace(input)
	low := strings.ToLower(in)
	if strings.HasPrefix(low, "curl ") || strings.Contains(low, "curl ") && strings.Contains(low, "-h ") {
		// 从 curl 里找 cookie 头（单双引号都支持）
		for _, re := range []string{
			`(?i)-H\s+'cookie:\s*([^']+)'`,
			`(?i)-H\s+"cookie:\s*([^"]+)"`,
			`(?i)--cookie\s+'([^']+)'`,
			`(?i)--cookie\s+"([^"]+)"`,
			`(?i)-b\s+'([^']+)'`,
			`(?i)-b\s+"([^"]+)"`,
		} {
			if m, _ := regexp.Compile(re); m != nil {
				if sub := m.FindStringSubmatch(in); len(sub) > 1 {
					return strings.TrimSpace(sub[1]), nil
				}
			}
		}
		return "", fmt.Errorf("从 cURL 里没找到 cookie 头，请直接粘贴 Cookie 字符串")
	}
	// 去掉前缀 "Cookie:"，规整换行/空格
	c := strings.TrimSpace(in)
	c = strings.TrimPrefix(strings.TrimPrefix(c, "Cookie:"), "cookie:")
	c = strings.ReplaceAll(c, "\n", "; ")
	c = strings.ReplaceAll(c, "\r", "")
	c = strings.Join(strings.Fields(c), " ")
	if !strings.Contains(c, "=") {
		return "", fmt.Errorf("这不像 Cookie（缺少 name=value），请从浏览器 Network → 任意请求 → Request Headers → Cookie 复制")
	}
	return c, nil
}
