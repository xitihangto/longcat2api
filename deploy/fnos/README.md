# 在飞牛 OS（fnOS）上用 Docker Compose 部署 longcat2api

> 目标：飞牛 NAS 上跑一个 `http://<NAS内网IP>:8090/v1` 的 OpenAI 兼容接口，Cherry Studio / NextChat / OpenWebUI 都能直接接。
> 全部操作只需要：飞牛的「Docker」应用 + 一份 `accounts.json`。**不需要在 NAS 上装 Go、编译、登录妙手。**

---

## 0. 前置检查（30 秒）

1. 飞牛 Web 管理后台 →**应用中心**→ 装好「**Docker**」应用并启动。
2. 记下你的**存储空间路径**：飞牛一般是 `/vol1/1000`（用户主目录）。
   在「文件管理」里点开任意目录 → 地址栏显示的 `/vol1/1000/xxx` 就是真实路径。
   本文统一按 `/vol1/1000/docker/longcat2api` 写，**不一致就整篇替换**。
3. 确认 8090 端口没被占用（飞牛自身用 5666/5667/8000 等，一般不会冲突）。

---

## 1. 先在 Mac 上把账号准备好（一次性）

容器里读不到妙手客户端的加密凭据（机器标识不同），所以账号必须先在 Mac 上导出：

```bash
cd ~/DSH/longcat2api
go run ./cmd/login          # 解密本机客户端凭据 → 校验 → 写入 ./data/accounts.json
cat data/accounts.json      # 应该看到 uid / name / token
```

拿到两样东西：
- `~/DSH/longcat2api/data/accounts.json`（**推荐**，直接拷过去）
- 或者 `accounts.json` 里的 `token` 字符串（`AgGF...`，用于注入方式）

---

## 2. 建目录

**图形方式**：文件管理 → 进入 `/vol1/1000/docker`（没有就新建 `docker` 文件夹）→ 新建文件夹 `longcat2api` → 进去再建两个子文件夹 `config`、`data`。

**SSH 方式**（更好用，建议开）：

```bash
# 飞牛：设置 → SSH → 开启；然后用你的飞牛账号登录
ssh <你的用户名>@<NAS内网IP>

mkdir -p /vol1/1000/docker/longcat2api/{config,data}
```

---

## 3. 放账号文件

任选一种：

**A. 拷贝 accounts.json（推荐）**

```bash
# 在 Mac 上执行
scp ~/DSH/longcat2api/data/accounts.json <用户名>@<NAS内网IP>:/vol1/1000/docker/longcat2api/data/
```

不用 SSH 就：飞牛「文件管理」→ 进到 `docker/longcat2api/data` → 上传 → 选 `accounts.json`。

**B. 用 token 注入**（先用 A 之外的方式拿到 token）

第 5 步起服务后执行一次：

```bash
docker compose run --rm login -token 'AgGF...'
```

（`login` 服务在第 4 步的 compose 里已经定义好，用 `--profile` 隐藏，不影响常驻服务。）

---

## 4. 建 Compose 项目

### 方式一：Docker 图形界面（推荐新手）

1. 飞牛 →**Docker**→ 左侧「**项目**」→「**新增项目**」
2. 项目名称：`longcat2api`
3. 路径：选 `/vol1/1000/docker/longcat2api`
4. 把 `deploy/fnos/docker-compose.yml` 的内容**整段粘贴**进去
5. **改一处**：`LONGCAT_API_KEY: "sk-longcat-change-me"` → 换成你自己的 key（例如 `sk-longcat-8f3a1c`）
6. 勾选「创建后立即启动」→ 确定

> 图形界面会自己处理 `pull → create → start`。第一次拉镜像约几十 MB，国内走南京大学源大概 1 分钟内。
>
> 如果你的飞牛 Docker 版本不认 `profiles`（报错里带 profiles 字样），把 compose 里最后那段 `login:` 服务整段删掉即可——
> 它只是可选的 token 注入工具，不影响常驻服务。

### 方式二：SSH 命令行

```bash
cd /vol1/1000/docker/longcat2api

# 拉这份 compose（或自己 vim 粘贴一份）
curl -fsSL https://raw.githubusercontent.com/xitihangto/longcat2api/main/deploy/fnos/docker-compose.yml -o docker-compose.yml

# 改 key
sed -i 's/sk-longcat-change-me/你的key/' docker-compose.yml

docker compose pull
docker compose up -d
docker compose ps
```

> 飞牛老版本的命令可能是 `docker-compose`（带横杠）而不是 `docker compose`，报 `unknown command` 就换一下。

---

## 4.5 可选：启用免费 LongCat-2.5（国际站）

0.3.0 起网关自带上第二条通道（`longcat.ai` 网页版，免费 LongCat-2.5）：

- **免登录**就能用（游客配额，容易限流）
- 想要稳定：浏览器登录 `https://longcat.ai/chat`（邮箱登录）→ F12 → 任意请求 → 右键 **Copy as cURL**
  → 打开面板 `http://<NAS>:8090/panel?key=<你的key>` → 「② 粘贴 Cookie」→ 「③ 校验并保存」
- 客户端里模型名写 `LongCat-2.5`（或 `2.5`）即走这条；写 `LongCat-2.0` 走妙手

## 5. 验收（四条命令，全过就算成功）

```bash
# ① 容器在跑且 healthy
docker ps --filter name=longcat2api --format '{{.Status}}'
#   期望：Up X minutes (healthy)

# ② 服务活着，且账号已加载（accounts 应该 ≥1）
curl -s http://127.0.0.1:8090/healthz
#   期望：{"accounts":1,"service":"longcat2api","status":"ok",...}

# ③ 鉴权生效
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8090/v1/models
#   期望：401（没带 key）

# ④ 真跑一轮对话
curl -s http://127.0.0.1:8090/v1/chat/completions \
  -H 'Authorization: Bearer 你的key' -H 'Content-Type: application/json' \
  -d '{"model":"longcat","messages":[{"role":"user","content":"只回复：nas-ok"}]}'
#   期望：..."content":"nas-ok"...
```

然后从局域网里任意设备访问 `http://<NAS内网IP>:8090` 会看到网关首页，`/panel` 是运维面板。

**接到客户端**（Cherry Studio / NextChat / OpenWebUI 都一样）：

| 项 | 值 |
|---|---|
| API Base / 接口地址 | `http://<NAS内网IP>:8090/v1` |
| API Key | 你在 compose 里设的 `LONGCAT_API_KEY` |
| 模型 | `LongCat-2.0`（或别名 `longcat`） |

---

## 6. 日常运维

```bash
cd /vol1/1000/docker/longcat2api

docker compose logs -f --tail=100 longcat2api   # 看日志
docker compose restart longcat2api              # 重启
docker compose pull && docker compose up -d     # 升级到最新镜像
```

- **开机自启**：`restart: unless-stopped` 已配好；飞牛 Docker 项目页也有「自动启动」开关，保持开启即可。
- **改配置**：编辑 `/vol1/1000/docker/longcat2api/config/config.json` 后 `docker compose restart`。
  想加多个账号，直接往 `data/accounts.json` 数组里加对象再重启。
- **token 过期**（上游返回 401 / 日志里 `upstream auth`）：
  在 Mac 上重新 `go run ./cmd/login` → 再 `scp` 覆盖一次 `data/accounts.json` → `docker compose restart`。

---

## 7. 常见问题

**① 拉镜像卡住 / 超时**
换加速源，编辑 compose 里 `image:` 那一行：

| 源 | 写法 |
|---|---|
| 南京大学（默认，匿名） | `ghcr.nju.edu.cn/xitihangto/longcat2api:0.3.0` |
| 1ms.run | `ghcr.1ms.run/xitihangto/longcat2api:0.3.0` |
| dockerproxy | `ghcr.dockerproxy.net/xitihangto/longcat2api:0.3.0` |
| 官方（有代理时） | `ghcr.io/xitihangto/longcat2api:0.3.0` |

改完 `docker compose pull && docker compose up -d`。

**② `/healthz` 里 `accounts: 0`**
`data/accounts.json` 没放进去、放错目录、或者权限不对（非 root 用户读不到 0600 文件）。
检查：`ls -l /vol1/1000/docker/longcat2api/data/`，本 compose 用 `user: "0:0"` 一般不会踩。

**③ 对话返回 502 / 日志有 `upstream auth`**
Mac 上那枚 token 过期了 → 见上面「token 过期」重新导入。

**④ 端口 8090 被占用**
改 compose 的 `ports:` 左边那个数字（例如 `"18090:8090"`），容器内部不用动。

**⑤ 想让局域网外也能用**
不要直接把 8090 暴露到公网。飞牛自带远程访问/内网穿透，或自己在前面套一层 HTTPS 反代（Nginx Proxy Manager / Lucky），并且务必设置强 `LONGCAT_API_KEY`。

---

## 8. 安全提醒

- `data/accounts.json` 里是**明文妙手 token**，等同于你的登录态，别共享、别提交到 git、别放进公开分享。
- 这个网关只应给你自己的设备用；不要对外提供额度、不要批量注册小号。
- 容器以 root 运行是为了省去挂载权限麻烦；如果你在意，把 `user: "0:0"` 改成飞牛用户的 `user: "1000:1000"`，
  并执行 `sudo chown -R 1000:1000 /vol1/1000/docker/longcat2api`。
