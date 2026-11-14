# 妙手（CatPaw / longcat2api）客户端协议笔记

> 逆向对象：`/Applications/妙手.app`（`com.catx.catpaw`，Electron，`catpaw-moon` 2026.1110.1503）
> 所有结论均在本机实测通过（2026-11-14），仅用于个人账号接入自己的工具。

## 1. 三个域名，三套鉴权

妙手客户端并不是"一个网关"，而是三类服务，鉴权头各不相同：

| 用途 | 生产地址 | 鉴权方式 |
|---|---|---|
| 会话编排 `/api/chat/*` | `https://nocode.cn` | `access-token: <token>` |
| 网关 REST `/api/gateway/*` | `https://catx.nocode.cn` | `X-Auth-Token: <token>` |
| Agent 事件流 `/api/agent/stream/connect` | `https://ai.catpaw.meituan.com` | `X-Passport-Token: <token>`（可同时带 `Cookie: X-Passport-Token=<token>`） |

`<token>` 就是客户端存在 `catx-credential.json` 里、经 AES-256-GCM 加密的 **Meituan SSO access token**。
同一个 token 在三个域名上分别用不同 header 名，用了错的 header 会得到
`401 {"code":4010,"message":"Authentication token is required"}` 或
`{"code":401,"data":{"message":"未登录或登录失效，需要先登录"}}`。

其它对外相关地址（来自 `@catx/desk-channel-sdk`）：

- 网关 REST（external tenant）：`catx.nocode.cn`；企业版 `enterprise.catpaw.com`；出海 `global-gateway.catpaw.com`（agent `catpaw-agent.mykeeta.com`）
- 直连 agent：对内 `catpaw-agent.sankuai.com`（要 SSO `ssoid` cookie，**对外账号不可用**）、对外 `ai.catpaw.meituan.com`、测试 `ai-catpaw.test.meituan.com`

## 2. 凭证：`catx-credential.json`

macOS 路径：`~/Library/Application Support/catpaw-moon/catx-credential.json`

```json
{ "ssoTokenEnc": "base64(iv||tag||ciphertext)" }
```

- 算法：`AES-256-GCM`
- 密钥：`sha256("<machineId>:catpaw-desk-token-v2")`
  - macOS `machineId` = `ioreg -rd1 -c IOPlatformExpertDevice` 的 `IOPlatformUUID`，**小写**
  - Windows `machineId` = 注册表 `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid`
- 布局：`iv = buf[0:12]`、`tag = buf[12:28]`、`ciphertext = buf[28:]`
- 明文：`{"access_token":"AgGF...","modified_at":<ms>}`

`go run ./cmd/login` 已实现上述解密 + 校验 + 导入，无需手工复制 token。

## 3. 一轮对话 = 三步

### 3.1 建会话 `POST https://nocode.cn/api/chat/create`

```json
{
  "chatId": "desk-<8hex><8hex>",
  "source": "catdesk",
  "prompt": "<首条用户输入，服务端只用于生成标题，截断即可>",
  "images": [], "files": [],
  "type": "COMMON",
  "techStackTemplateId": "default",
  "isAgent": true
}
```

返回 `{"code":0,"data":{"chatId":"...","title":"新会话","misId":"passport_<uid>", ...}}`。

### 3.2 发起一轮 `POST https://nocode.cn/api/chat/agent-stream`

```json
{
  "chatId": "desk-...",
  "messages": [
    { "role": "user",
      "content": [{ "type": "text", "text": "<正文>" }],
      "createTime": "2026-11-14T03:06:40.428Z" }
  ],
  "files": [], "urls": [],
  "model": "LongCat-2.0",
  "manualToolName": "", "toolMessageId": "",
  "isAgent": true,
  "currentPath": "/",
  "techStackTemplateId": "default",
  "sourcePlatform": "web",
  "isImportProject": false,
  "frontCreateTime": 1794625600428
}
```

返回：

```json
{"code":0,"data":{"conversationId":"82a8d479-...","roundId":1,
 "streamMessageId":"5582048","userMessageId":"5582049","success":true}}
```

`conversationId` 是后续订阅的凭据；`model` 字段目前**不生效**（服务端按账号档位选模型，实测回 `LongCat-2.0` / `LongCat-2.0-Agent`）。

### 3.3 订阅事件流 `POST https://ai.catpaw.meituan.com/api/agent/stream/connect`

```json
{ "timestamp": 1794625603000, "conversationId": "<上一步的>", "messageIndex": 0, "incrementalOnly": true }
```

响应 `Content-Type: text/event-stream`，每行 `data:<json>`，`<json>` 只有两种外壳：

```jsonc
// A. 业务帧
{"headlessRemoteAgentResp": {
   "isHistoryMessage": true,        // 历史回放（重连时会重发）
   "historyMessages": [ /* user / assistant 消息数组 */ ],
   "message": { "type":"assistant", "messageId":"...", "modelDisplayName":"LongCat-2.0-Agent",
                "content":[{"type":"text","text":"<**增量**>"}], "finished": false },
   "currentStatus": "running",      // running / completed / error / canceled
   "roundInfo": [{"round":1,"creditCost":"0.00000000", ...}]
}}
// B. 终态帧
{"statusUpdate":"completed"}       // completed / canceled / error
```

**关键行为**

1. `message.content[].text` 是**增量**（delta），直接拼接即全文；长回答每 100ms 左右一片。
2. 第一帧固定是历史回放（`isHistoryMessage:true`，含本轮 user 消息），可据此做去重：
   重连时把 history 里的 assistant 全文与已累积文本做 `HasPrefix` 比对，只补发差值。
3. 上游**不会主动关连接**：收到终态帧必须自己断开，否则会一直挂到超时（本仓库早期 bug）。
4. 传输中断且没拿到终态帧时（"closed without terminal frame"），客户端会以同样的
   `messageIndex` 退避重连（1s/2s/3s，最多 3 次），靠历史回放续上。
5. 用户消息在服务端会被包一层 Agent 模板后再喂给模型：
   `#user-query\nHere is the user's actual question or request:\n<user-query>…</user-query>\n#current-route…`
   这是官方行为（`isAgent:true` 时），对普通问答无副作用。

## 3.5 longcat.chat 网页通道（免费 LongCat-2.5）

与妙手完全独立的一条线：**longcat.chat 网页前端的对话接口**，实测返回 `LongCat-2.5-Agent`。

### 3.5.1 两个模式

| 模式 | 端点 | 鉴权 | 额度 |
|---|---|---|---|
| `oversea`（默认） | `POST /api/v1/chat-completion-oversea-V2` | **无需任何凭据** | 网页游客配额，**限流很凶** |
| `cn` | `POST /api/v1/session-create` → `POST /api/v1/chat-completion-V2` | Cookie（登录态） | 账号额度，稳定得多 |

固定请求头（缺了会被拒）：

```
m-appkey: fe_com.sankuai.friday.fe.longcat
m-traceid: <毫秒时间戳+随机>
origin: https://longcat.chat
referer: https://longcat.chat/t
x-client-language: zh
x-requested-with: XMLHttpRequest
```

### 3.5.2 oversea 请求体

```jsonc
{
  "content": "<正文>",
  "agentId": "1",
  "messages": [
    { "role": "user", "events": [{"type":"userMsg","content":"<正文>","status":"FINISHED"}],
      "chatStatus": "FINISHED", "messageId": 12345678, "idType": "custom" },
    { "role": "assistant", "events": [], "chatStatus": "LOADING",
      "messageId": 12345679, "idType": "custom" }
  ],
  "reasonEnabled": 0,     // 1 = 输出思考
  "searchEnabled": 0,     // 1 = 上游联网搜索
  "regenerate": 0
}
```

### 3.5.3 cn 请求体

```
POST /api/v1/session-create   {"model":"","agentId":"1"}   → data.conversationId
POST /api/v1/chat-completion-V2 {"content","conversationId","agentId","reasonEnabled","searchEnabled",
                                 "regenerate":0,"parentMessageId":0,"files":[]}
```

登录态校验：`GET /api/v1/user-current` → `data.loginStatus`（1 = 已登录）。
无 Cookie 调 session-create 返回 `{"code":401,"message":"请求cookie信息为空"}`。

### 3.5.4 SSE 帧

```jsonc
{"conversationId":"...","model":"LongCat-2.5-Agent","event":{"type":"create"}}
{"model":"LongCat-2.5-Agent","event":{"type":"content","content":"你","status":"PROCESSING"}}  // 增量
{"model":"LongCat-2.5-Agent","event":{"type":"content","content":"","status":"FINISHED"}}
{"model":"LongCat-2.5-Agent","tokenInfo":{"promptTokens":449,"completionTokens":2},
 "event":{"type":"finish","finalContentX":"你好","usage":{...}},"lastOne":true}
```

- 正文增量在 `event.content`（`event.type=="content"`），末帧 `lastOne:true` → **必须自己断开**
- `tokenInfo` 是**上游真实 token 数**（妙手通道没有这个）
- `promptTokens` 基线约 449：服务端注入了自己的 agent 系统提示词

### 3.5.5 限流

连发 10 次左右就会开始返回（HTTP 200 + JSON，不是 SSE）：

```json
{"code":429,"message":"操作太快啦，请稍后再试～","data":null}
```

而且**冷却时间较长**（本项目实测约 10 分钟以上未恢复，疑似按 IP 累计配额）。
对策：串行 + 最小间隔（本项目默认 1500ms）、429 后本地冷却（默认 90s）、失败可回落妙手通道。

> ⚠️ 该通道是**网页对话接口**，不是可编排的模型 API：不吃 `tools`/`function calling`，
> 也没法接管工具执行。只能当"文本补全"用，不能当 agent 后端。

## 4. 客户端内部还有哪些链路（未接入）

- **Pike 长连接 + desk channel**：桌面端本地 `agent-host` 与云端通过 Pike(WebSocket) 通信，
  走 `@ee/agent-host` → `pikem0-bj.sankuai.com`。反代不需要它，直连 SSE 更简单。
- **AG-UI 事件转换**：桌面端把 SSE 帧转成 AG-UI 事件（`TEXT_MESSAGE_CONTENT` 等），
  由 `@catpaw-ui/catpaw-transform-util` 的 `IncrementalRawSseToStreamItem` 完成；
  我们只需 delta 文本 + 终态。
- **积分/订阅**：`GET /api/gateway/credit/balance`（`X-Auth-Token`）。可作为额度观测接入。

## 5. 逆向路径备忘（怎么找到的）

1. `npx asar extract` 或按 asar 头解析 `/Applications/妙手.app/Contents/Resources/app.asar`（427MB，27000+ 文件）。
2. `package.json` → `main: dist-electron/bootstrap.js`，依赖里 `@catx/desk-channel-sdk`、`@catpaw-ui/catpaw-nocode-adapter` 直接暴露协议。
3. `@catpaw/nocode-adapter/dist/index.js`：`/api/chat/create` + `/api/chat/agent-stream` + `/api/agent/stream/connect` 三条路径与请求体全在里面。
4. `@catx/desk-channel-sdk/dist/services/gateway/gatewayEnv.js`：租户 × 环境的域名表（对内/对外/企业/出海）。
5. `dist-electron/main-Cq67Nb0I.js` 里的 `prod-nocode-token` / `X-Passport-Token` / `catpaw-ep-token`：Cookie 与 header 名。
6. `dist-electron/storageService-CNmS7ZQl.js`：`ssoTokenEnc` 的 AES-256-GCM 加解密与 `:catpaw-desk-token-v2` 盐。
