package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linquin/longcat2api/internal/config"
)

// fakeLongcatWeb 模拟 longcat.chat 的 SSE：create → content* → finish
func fakeLongcatWeb(t *testing.T, calls *int, rateLimit bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Header.Get("m-appkey") == "" {
			t.Errorf("缺少 m-appkey 头")
		}
		if rateLimit {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":429,"message":"操作太快啦，请稍后再试～","data":null}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		frames := []string{
			`{"conversationId":"c1","model":"LongCat-2.5-Agent","event":{"type":"create"}}`,
			`{"conversationId":"c1","model":"LongCat-2.5-Agent","event":{"type":"content","content":"你","status":"PROCESSING"}}`,
			`{"conversationId":"c1","model":"LongCat-2.5-Agent","event":{"type":"content","content":"好","status":"PROCESSING"}}`,
			`{"conversationId":"c1","model":"LongCat-2.5-Agent","event":{"type":"content","content":"","status":"FINISHED"}}`,
			`{"conversationId":"c1","model":"LongCat-2.5-Agent","tokenInfo":{"promptTokens":449,"completionTokens":2,"totalTokens":451},"event":{"type":"finish","finalContentX":"你好"},"lastOne":true}`,
			`{"conversationId":"c1","event":{"type":"content","content":"不该出现"}}`,
		}
		for _, f := range frames {
			fmt.Fprintf(w, "data:%s\n\n", f)
			fl.Flush()
		}
	}))
}

func TestLongcatWebChat(t *testing.T) {
	calls := 0
	srv := fakeLongcatWeb(t, &calls, false)
	defer srv.Close()

	cfg := config.Default()
	cfg.LongcatWeb.Base = srv.URL
	cfg.LongcatWeb.MinIntervalMS = 0

	var deltas []string
	res, err := NewLongcatWeb(cfg).Chat(context.Background(), "你好", "LongCat-2.5",
		func(s string) error { deltas = append(deltas, s); return nil }, nil)
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if res.Text != "你好" {
		t.Errorf("正文错误: %q", res.Text)
	}
	// 收到 lastOne 后必须主动断开，不能把后续帧也算进回答
	if got := strings.Join(deltas, ""); got != "你好" {
		t.Errorf("增量错误: %q", got)
	}
	if res.Model != "LongCat-2.5-Agent" {
		t.Errorf("模型回填错误: %q", res.Model)
	}
	if res.PromptTokens != 449 || res.CompletionTokens != 2 {
		t.Errorf("真实用量未取到: %+v", res)
	}
	if res.Status != "completed" {
		t.Errorf("状态错误: %q", res.Status)
	}
}

func TestLongcatWebRateLimitCooldown(t *testing.T) {
	calls := 0
	srv := fakeLongcatWeb(t, &calls, true)
	defer srv.Close()

	cfg := config.Default()
	cfg.LongcatWeb.Base = srv.URL
	cfg.LongcatWeb.MinIntervalMS = 0
	cfg.LongcatWeb.Cooldown = "60s"
	c := NewLongcatWeb(cfg)

	_, err := c.Chat(context.Background(), "hi", "LongCat-2.5", nil, nil)
	if err == nil {
		t.Fatal("期望限流错误")
	}
	var ue *Error
	if !asErr(err, &ue) || ue.Kind != KindRateLimit {
		t.Fatalf("期望 KindRateLimit，得到 %v", err)
	}
	// 冷却期内第二次请求应直接短路，不再打到上游
	before := calls
	_, err2 := c.Chat(context.Background(), "hi", "LongCat-2.5", nil, nil)
	if err2 == nil {
		t.Fatal("冷却期内应直接返回限流错误")
	}
	if calls != before {
		t.Errorf("冷却期内不应再请求上游，calls %d → %d", before, calls)
	}
}
