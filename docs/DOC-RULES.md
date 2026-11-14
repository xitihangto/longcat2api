# 文档规范（命令块）

目标是：**读者能把文档里的命令块整段复制粘贴就成功**。为此定几条硬规矩，CI 会自动检查。

## 1. 注释独立成行，不要写在命令后面

```text
# ❌ 反例（仅示意，标 text 以免被检查器误判）
cd ~/DSH/longcat2api          # 换成你的路径
go run ./cmd/server           # 启动
```

```bash
# ✅ 正例
# 换成你的路径
cd ~/DSH/longcat2api

# 启动
go run ./cmd/server
```

原因：`#` 只有前面有空格才被当注释，**zsh 需开启 `interactive_comments`**（不少人关着），
PowerShell / cmd 更是直接报错。而 `cd path  # 注释` 失败时往往看不出是注释惹的祸。

## 2. 默认从仓库根目录执行

命令块假定 cwd 是仓库根（能看到 `cmd/`、`scripts/` 的那层）。需要切换目录时，
`cd` 单独一行写在最前面，并在块外用一句话说明路径怎么找。

## 3. 占位符统一写作 `<说明>`

```bash
curl -X POST http://192.168.1.10:8090/panel/api/channel/login \
  -H 'Authorization: Bearer <你的 api_key>'
```

不要写成 `Bearer 你的key` 这种半中文形式，容易连占位符一起复制进去。

## 4. 代码块要标语言

`bash` / `sh` / `zsh` / `console` 会按 shell 规范检查；`yaml` / `json` / `jsonc` / `text`
不套用 shell 规则。不标语言时按 shell 处理。

## 5. 版本号写范围，不写死

文档里用 `0.3.x` 这类写法描述"当前版本行为"，避免每发一个小版本都要改一堆文档；
确需精确版本（如镜像 tag 示例）时写明"示例"。

## 6. 提交前自检

```bash
./scripts/check-docs.sh
```

CI（`.github/workflows/docker-ghcr.yml` 的 `test` 任务）也会跑这个检查，违规会直接失败。

## 其它既有约定

- 面向用户的文案**不出现内部黑话**（例如通道模式统一叫 `guest` / `auth`，不要写 `cn` / `oversea`）。
- 站点、路径这类会变的信息，从配置推导后再展示，不要在 HTML/文案里写死。
- 任何"改坏了会让人误解"的行为（限流回落、通道切换），都要在响应头或日志里留痕。
