package server

import (
	"strings"
	"testing"
)

func msg(role, text string) chatMsgIn {
	b, _ := jsonMarshal(text)
	return chatMsgIn{Role: role, Content: b}
}

func TestBuildPromptSingleTurn(t *testing.T) {
	got, err := buildPrompt([]chatMsgIn{msg("user", "你好")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "你好" {
		t.Fatalf("单轮应原样下发，得到 %q", got)
	}
}

func TestBuildPromptSystemAndHistory(t *testing.T) {
	got, err := buildPrompt([]chatMsgIn{
		msg("system", "你是助手"),
		msg("user", "第一问"),
		msg("assistant", "第一答"),
		msg("user", "第二问"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"你是助手", "用户：第一问", "助手：第一答", "第二问"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q，实际: %s", want, got)
		}
	}
	if strings.HasSuffix(got, "第一答\n") {
		t.Errorf("最后一轮应是用户消息")
	}
}

func TestBuildPromptEmpty(t *testing.T) {
	if _, err := buildPrompt([]chatMsgIn{msg("system", "只有系统提示")}); err == nil {
		t.Fatal("没有 user 内容时应报错")
	}
}

func TestResolveModelAlias(t *testing.T) {
	s := &Server{cfg: configDefaultForTest()}
	if got, err := s.resolveModel("longcat"); err != nil || got != "LongCat-2.0" {
		t.Fatalf("别名解析失败: %q %v", got, err)
	}
	if got, err := s.resolveModel("unknown-model"); err != nil || got != "unknown-model" {
		t.Fatalf("未知模型应透传: %q %v", got, err)
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("空串应为 0")
	}
	if n := estimateTokens("你好世界"); n < 1 {
		t.Fatalf("中文估算异常: %d", n)
	}
}
