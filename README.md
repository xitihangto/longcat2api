# longcat2api

把 **妙手（CatPaw / LongCat）桌面客户端**的登录态反代成 **OpenAI 兼容 API** 的自托管网关。

> 灵感与工程结构参照 [workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel)（Go + 账号池 + 面板）。
> 上游协议为本人实测逆向，笔记见 [docs/PROTOCOL.md](docs/PROTOCOL.md)。
>
> ⚠️ **仅供本人账号、本机/私有环境自用**。请勿批量注册、对外售卖额度或二次分发，相关风险与作者无关。

## 现状（v0.1.0）

| 能力 | 状态 |
|---|---|
| `POST /v1/chat/completions` 非流式 | ✅ 实测通过 |
| `POST /v1/chat/completions` 流式（SSE，真增量） | ✅ 实测通过 |
| `GET /v1/models` | ✅ |
| `GET /healthz` / `/panel/api/*` 观测接口 | ✅ |
| 妙手登录态一键导入（本机凭据解密） | ✅ `go run ./cmd/login` |
| 多账号池 + 熔断/冷却/在途负载 | ✅ |
| Web 面板 UI | ⏳ 待做（接口已就绪） |
| 工具调用 / 多模态 / 联网搜索 | ⏳ 未接入 |
| token 自动续期 | ⏳ 需重新登录导入 |

## 快速开始

```bash
# 0) 依赖：Go 1.22+；本机已登录妙手客户端
cd longcat2api

# 1) 生成配置（首次运行自动写入推荐配置）
go run ./cmd/server -config ./config.json     # 首次会生成 config.json 后启动

# 2) 导入账号：从客户端凭据解密 + 校验 + 写入 data/accounts.json
go run ./cmd/login

# 3) 启动
go run ./cmd/server
# longcat2api 0.1.0 启动 → http://127.0.0.1:8090  (账号 1 个)

# 4) 调用
curl -N http://127.0.0.1:8090/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"longcat","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

任意 OpenAI SDK / Cherry Studio / NextChat 只要把 Base URL 改成
`http://127.0.0.1:8090/v1`，模型填 `LongCat-2.0`（或别名 `longcat`）即可。

## 配置说明（config.json）

```jsonc
{
  "listen": ":8090",
  "api_key": "",                  // 非空则要求 Authorization: Bearer <key>
  "accounts_file": "./data/accounts.json",
  "state_file": "./data/state.json",
  "upstream": {
    "chat_base":   "https://nocode.cn",              // /api/chat/*
    "gateway_base":"https://catx.nocode.cn",          // /api/gateway/*
    "stream_base": "https://ai.catpaw.meituan.com",   // /api/agent/stream/connect
    "model": "LongCat-2.0",
    "timeout_seconds": 300,
    "first_byte_seconds": 45,
    "connect_retries": 3
  },
  "cooldown": { "soft_rate": "600s", "soft_rate_max": "2h", "auth_fail": "30m" },
  "models": { "catalog": [ ... ], "alias": { "longcat": "LongCat-2.0" } }
}
```

## 账号文件

`data/accounts.json`（0600）：

```json
[{ "id":"acc-1", "name":"gau…", "token":"AgGF…", "uid":"3522699764", "enabled":true }]
```

token 即妙手客户端用的 **SSO access token**，三个域名分别用
`access-token` / `X-Auth-Token` / `X-Passport-Token` 头携带（见协议笔记）。
token 过期后重新执行 `go run ./cmd/login` 即可（同一 uid 会覆盖原记录）。

## 网关行为

- **选号**：健康账号中按「在途数最少 → 最久未使用」挑选；失败可换号重试一次。
- **冷却**：401 → `auth_fail`（默认 30m）；429/限流 → 指数退避（600s 起，封顶 2h）；
  额度类错误 → 冷却到次日 04:00；其它连续 3 次失败 → 60s。
- **提示词拼装**：妙手单轮只接受一条 user 文本，因此 `system` 与历史轮次会被拼进同一条消息；
  单轮纯提问时正文与原始输入完全一致。
- **用量**：上游不返回 token 数，`usage` 为按字符粗估，仅作量级参考。
- **流式**：上游帧本身是增量，网关逐帧转发，不缓冲整段。

## 目录结构

```
cmd/server        网关入口
cmd/login         妙手登录态导入（AES-256-GCM 解密 catx-credential.json）
internal/config   配置加载/生成
internal/account  账号存储（accounts.json）
internal/upstream 妙手协议客户端（create → agent-stream → SSE connect）
internal/pool     账号池：选号、在途、熔断冷却、状态持久化
internal/server   OpenAI 兼容层（/v1/chat/completions、/v1/models、/panel/api/*）
docs/PROTOCOL.md  逆向协议笔记
```

## 免责声明

本项目仅把**你自己账号**的客户端能力接到你自己的工具上，属于个人自用工具。
妙手/LongCat 的服务条款可能限制非官方客户端访问，由此产生的账号风险由使用者自行承担。
请勿用于批量注册、额度转售、加壳收费分发。
