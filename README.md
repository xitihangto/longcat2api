# longcat2api

<p align="left">
  <a href="https://github.com/xitihangto/longcat2api/actions/workflows/docker-ghcr.yml"><img alt="docker-ghcr" src="https://github.com/xitihangto/longcat2api/actions/workflows/docker-ghcr.yml/badge.svg"></a>
  <a href="https://github.com/xitihangto/longcat2api/pkgs/container/longcat2api"><img alt="ghcr" src="https://img.shields.io/badge/ghcr.io-longcat2api-2496ED?logo=docker&logoColor=white"></a>
  <a href="https://github.com/xitihangto/longcat2api/releases/latest"><img alt="release" src="https://img.shields.io/github/v/release/xitihangto/longcat2api?color=3ecf8e"></a>
  <img alt="Go" src="https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white">
  <img alt="API" src="https://img.shields.io/badge/API-OpenAI_Compatible-412991">
</p>

把 **妙手（CatPaw / LongCat）桌面客户端**的登录态反代成 **OpenAI 兼容 API** 的自托管网关。

> 灵感与工程结构参照 [workbuddy2api-panel](https://github.com/linguo2625469/workbuddy2api-panel)（Go + 账号池 + 面板）。
> 上游协议为本人实测逆向，笔记见 [docs/PROTOCOL.md](docs/PROTOCOL.md)。
>
> ⚠️ **仅供本人账号、本机/私有环境自用**。请勿批量注册、对外售卖额度或二次分发，相关风险与作者无关。

## 现状（v0.3.0）

| 能力 | 状态 |
|---|---|
| 双通道：妙手 2.0 + longcat-web 2.5 | ✅ 按模型名自动路由 |
| `POST /v1/chat/completions` 非流式 | ✅ 实测通过 |
| `POST /v1/chat/completions` 流式（SSE，真增量） | ✅ 实测通过 |
| `GET /v1/models` | ✅ 列出 `LongCat-2.5`（免费网页通道）与 `LongCat-2.0`（妙手） |
| `GET /healthz` / `/panel/api/*` 观测接口 | ✅ |
| 妙手登录态一键导入（本机凭据解密） | ✅ `go run ./cmd/login` |
| longcat 登录态（Cookie）导入 | ✅ 面板粘贴 / Copy as cURL / `scripts/import-chrome-cookie.mjs` |
| 多账号池 + 熔断/冷却/在途负载 | ✅ |
| Docker / docker compose 部署 | ✅ Dockerfile + compose + 健康检查 |
| Web 面板 UI | ✅ 最简版（`/panel`：双通道状态 + 账号池 + 概览，15s 自刷；`/panel?key=<key>` 可直达） |
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
# longcat2api 0.3.0 启动 → http://127.0.0.1:8090  (账号 1 个)

# 4) 调用
curl -N http://127.0.0.1:8090/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"longcat","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

任意 OpenAI SDK / Cherry Studio / NextChat 只要把 Base URL 改成
`http://127.0.0.1:8090/v1`，模型填 `LongCat-2.0`（或别名 `longcat`）即可。

面板同理受 `api_key` 保护：浏览器直接开 `/panel?key=<你的 api_key>` 会自动记住 key；
手动进入则在页面输入框里填同一个 key 再点「保存并刷新」。

## 两条通道

| 模型 | 通道 | 凭据 | 说明 |
|---|---|---|---|
| `LongCat-2.5` | longcat **国际站**网页通道（`longcat.ai`） | **免登录**（可选 Cookie） | 免费，上游限流较凶；有真实 token 用量 |
| `LongCat-2.0` | 妙手 CatPaw 账号池 | 妙手登录态（`cmd/login` 导入） | 稳定不限流，模型固定 2.0 |

客户端只写模型名：`LongCat-2.5` / `longcat-2.5` / `2.5` 走免费 2.5；`LongCat-2.0` / `longcat` 走妙手。
2.5 被限流时会自动回落到 2.0（`longcat_web.fallback_to_catpaw`），响应头 `X-Longcat-Channel` 标明实际通道。

> ⚠️ 两条通道都只提供**文本补全**：不吃 `tools` / `function calling`，不能当 Claude Code 那类 agent 的后端。

## 导入妙手账号（LongCat-2.0 通道）

三种方式，都不需要在 NAS 上装任何东西：

**A. 面板粘贴（推荐）**

面板顶部「妙手账号」卡片 → 粘贴 token → 「校验并导入」。支持：

- 裸 token（`AgGF…`）
- 带 `access-token:` / `X-Auth-Token:` / `Authorization: Bearer` 头的 **Copy as cURL** 整段
- 含 `"access_token":"…"` 的 JSON

校验走上游 `current-user`，成功即入池并**立即生效**（无需重启）。

**B. 宿主机导入**

```bash
go run ./cmd/login                 # Mac/PC 上已登录妙手客户端：自动解密 + 校验 + 入库
go run ./cmd/login -token 'AgGF…'  # 或直接给 token / cURL
```

**C. 容器内工具容器**

```bash
docker compose run --rm login -token 'AgGF…'
docker compose run --rm login -check      # 巡检现有账号
```

> token 从哪来：Mac 上跑一次 `go run ./cmd/login`，然后从 `data/accounts.json` 里复制 `token` 字段。

## 登录 longcat（拿 Cookie）

只有**一次**操作，cookie 有效期很长，之后不用再管。三条路，按方便程度：

**A. 浏览器 F12（最通用）**

1. 打开 `https://longcat.ai/chat` → 点「请先登录」→ 邮箱登录（`passport.mykeeta.com`，Continue with email）
2. F12 → Network → 任意请求 → 右键 **Copy as cURL**（或复制 Request Headers 里的整串 `Cookie`）
3. 粘进面板「② 粘贴 Cookie」→ 点「③ 校验并保存」

面板会调上游校验，成功即热生效并写入配置，**不用重启容器**。支持整串 Cookie、`Cookie:` 前缀、以及整段 cURL。

**B. Mac 上懒人版（一条命令）**

```bash
node scripts/import-chrome-cookie.mjs      # 从本机 Chrome 解密出 longcat cookie → /tmp/lc-cookie.txt
```
把文件内容粘进面板即可（不需要装任何东西，用系统自带的 node 就行）。

**C. 命令行直接灌**

```bash
curl -X POST http://<网关>/panel/api/channel/login \
  -H "Authorization: Bearer <api_key>" -H 'Content-Type: application/json' \
  -d "$(python3 -c 'import json;print(json.dumps({"cookie":open("/tmp/lc-cookie.txt").read()}))')"
```

> 之前做过一版「内置远程浏览器扫码登录」，镜像要 1.8GB 而且帧率低，已移除 —— 一次性登录不值这个体积。

## Docker 部署

镜像发布在 GitHub Container Registry（amd64 / arm64 多架构，跟随 `main` 自动构建）。

### 拉取慢？换加速站

`docker-compose.yml` 里 image 是 `${IMAGE}:${TAG}`，只改 `IMAGE` 就能走加速：

| 源 | `IMAGE=` | 说明 |
|---|---|---|
| 官方 GHCR | `ghcr.io/xitihangto/longcat2api` | 海外/有代理时最快 |
| 1ms.run | `ghcr.1ms.run/xitihangto/longcat2api` | 国内加速，实测可拉双架构 |
| 南京大学 | `ghcr.nju.edu.cn/xitihangto/longcat2api` | 匿名可用，速度快 |
| dockerproxy | `ghcr.dockerproxy.net/xitihangto/longcat2api` | 匿名可用，较慢但稳 |

不想手挑就自动试：

```bash
./scripts/pull-image.sh        # 逐个 docker pull，成功者把 IMAGE 写回 .env
```

### 起服务

```bash
cp .env.example .env                        # 填 LONGCAT_API_KEY，按需改 IMAGE
cp config.example.json config/config.json   # 想改端口/模型再编辑
docker compose pull && docker compose up -d
docker compose logs -f longcat2api
curl -s http://127.0.0.1:8090/healthz
```

本地构建（不用镜像）：

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
# 基础镜像拉不动时在 .env 里换：BASE_GO/BASE_RUN=docker.m.daocloud.io/library/...
```

| 项 | 值 |
|---|---|
| 镜像 | `ghcr.io/xitihangto/longcat2api`（tag：`latest` / `0.3.0`=`v0.3.0` / `sha-<短哈希>`） |
| 发版 | [v0.3.0](https://github.com/xitihangto/longcat2api/releases/tag/v0.3.0)（`git tag -a vX.Y.Z && git push origin vX.Y.Z` 即自动出多架构镜像） |
| 平台 | `linux/amd64`, `linux/arm64` |
| 自动构建 | 推 `v*` tag 或合入 `main` 触发 `.github/workflows/docker-ghcr.yml`（先跑 go vet + go test） |

> GHCR 包若改成私有：`echo <PAT> | docker login ghcr.io -u <用户名> --password-stdin`，PAT 只需 `read:packages`。

### 账号怎么进容器

容器里**没法**解密客户端凭据（机器标识不同），所以 token 必须由宿主机提供，两条路：

**A. 宿主机导入（推荐，长期用）**

```bash
# 本机已登录妙手客户端时
go run ./cmd/login                 # 解密 + 校验 + 写入 ./data/accounts.json
docker compose restart longcat2api # 重启加载账号（或首次 up 时即已挂载）
```

**B. 容器内注入（最快，试水用）**

```bash
# 1) 先把 token 拿到手：宿主机跑 go run ./cmd/login，或从 ./data/accounts.json 里取
# 2) 写进 .env：LONGCAT_TOKEN=AgGF...
docker compose up -d --build       # 首次启动检测到无账号 → 自动校验并写入 /app/data/accounts.json
# 3) 导入成功后可以把 .env 里的 LONGCAT_TOKEN 删掉
```

也可以在容器内跑工具容器：

```bash
docker compose run --rm login -token AgGF...   # 导入指定 token
docker compose run --rm login -check           # 巡检已有账号是否有效
```

### 目录挂载

| 宿主机 | 容器 | 说明 |
|---|---|---|
| `./config/` | `/app/config` | 配置目录；`config.json` 不存在时程序会自动生成完整默认配置 |
| `./data/` | `/app/data` | `accounts.json`（账号）+ `state.json`（冷却状态） |

### 环境变量（覆盖 config.json，无需改文件）

| 变量 | 说明 |
|---|---|
| `LONGCAT_API_KEY` | 对外 Bearer key，非空则覆盖配置 |
| `LONGCAT_LISTEN` | 监听地址，默认 `:8090` |
| `LONGCAT_MODEL` | 默认模型名 |
| `LONGCAT_CHAT_BASE` / `LONGCAT_STREAM_BASE` / `LONGCAT_GATEWAY_BASE` | 上游三域名（换环境/自建代理时用） |
| `LONGCAT_ACCOUNTS_FILE` / `LONGCAT_STATE_FILE` | 账号/状态路径 |
| `LONGCAT_TIMEOUT_SECONDS` / `LONGCAT_FIRST_BYTE_SECONDS` | 超时 |
| `LONGCAT_LOG_REQUESTS` | 是否打印每请求日志 |
| `LONGCAT_TOKEN` | 仅首次启动注入账号用 |
| `PORT` / `PUID` / `PGID` / `TZ` / `GOPROXY` / `TAG` | compose 层面：映射端口、运行用户、时区、构建代理、镜像 tag |

### 挂载权限（常见坑）

**默认容器以 root 运行**（compose 里 `user: "${PUID:-0}:${PGID:-0}"`），
这样 `./data/accounts.json`（0600）能直接读、`./config` 能自动生成配置，开箱即用。

想收紧成非 root：

```bash
echo "PUID=$(id -u)"$'\n'"PGID=$(id -g)" >> .env
sudo chown -R "$(id -u):$(id -g)" ./data ./config   # 属主要和 PUID/PGID 一致
docker compose up -d
```

非 root 时最典型的两个症状：读不到 `accounts.json` → `/healthz` 里 `accounts: 0`；
写不了 `state.json` → 冷却状态不持久（不影响对话）。

### 多架构

`Dockerfile` 交叉编译 `linux/amd64` 与 `linux/arm64`（CGO 关闭的静态二进制）：

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t yourname/longcat2api:0.3.0 --push .
```

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
