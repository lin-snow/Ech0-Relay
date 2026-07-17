# Ech0-Relay

基于 [Ech0](https://github.com/lin-snow/Ech0) 的多实例自动化推文管理服务。

把**公开 Telegram 频道**的新帖子，定时自动镜像进一个或多个 Ech0 实例。整个服务是一个无状态的 Go 二进制，由 GitHub Actions 定时驱动，通过 Ech0 的公开 REST API + 作用域化 access token 发帖，去重游标以文件形式提交回本仓库。

## 它能做什么

- **多实例**：一份配置管理多个 Ech0 实例，各用各的 access token。
- **公开频道同步**：抓取 `t.me/s/<频道>` 的公开网页，无需任何 Telegram 凭证；把文本转成 markdown，并用原帖时间回填 `created_at`（时间线不乱）。
- **图片托管（可选）**：默认以 CDN 直链嵌入图片；开启 `upload_images` 后改为下载图片并上传到 Ech0 本地存储（localfs），以 waterfall 布局挂载到帖子——图片永久留存，删帖时级联清理。
- **按频道保留清理（可选）**：每个频道最多保留 N 条，超出后自动删除最旧的——**只删带来源标签的帖子，绝不碰你手写的内容**。
- **稳健去重**：每个频道记录 `last_message_id`，只同步更新的帖子；失败即停、下轮续跑，不留缺口。

## 工作原理

```
GitHub Actions (cron)                 本仓库                    你的 Ech0 实例
────────────────────                  ──────                    ─────────────
sync.yml 每 30 分钟触发
  └─ 下载最新 release 二进制  ◀── build.yml 打 tag 时构建上传
  └─ 运行 relay
        ├─ 抓 t.me/s/<频道>  ─────────────────────────────────▶ (只读公开网页)
        ├─ 过滤 id > 游标
        ├─ POST /api/echo  ──────────────────────────────────▶ 发帖（带标签、回填时间）
        ├─ 保留清理（可选）  ──────────────────────────────────▶ 查询 + 删除最旧
        └─ 写游标 state/state.json
  └─ git commit state 回仓  ──▶ state/state.json
```

- **构建**：`build.yml` 在打 `v*` tag 时交叉编译 `linux/amd64` 二进制并发布为 **非 draft** 的 GitHub Release。
- **同步**：`sync.yml` 定时下载最新 release 二进制运行，跑完把游标提交回仓。

## 快速上手

### 1. 在 Ech0 里创建 access token

在目标 Ech0 实例的管理后台创建一个 access token：

- **作用域**：`echo:read` + `echo:write`（读用于保留清理的计数/查询，写用于发帖和删除）；开启 `upload_images` 时还需 `file:write`（上传图片及失败时的清理）
- **audience**：`integration`
- 归属用户需为 **admin**（Ech0 的删除接口受 `IsAdmin` 限制）

> ⚠️ 保留清理会删除帖子。Ech0 的删除接口只校验 admin、不校验单条归属，因此“删哪些”完全由本服务的**标签过滤**决定。请务必给启用清理的同步项配置唯一的 `tag`。

### 2. 配置 `config.yaml`

复制模板并按需修改（`config.yaml` 不含密钥，提交进仓库即可）：

```bash
cp config.example.yaml config.yaml
```

完整字段说明见 [`config.example.yaml`](./config.example.yaml)。最小示例：

```yaml
instances:
  myblog:
    base_url: https://echo.example.com
    token_env: ECH0_MYBLOG_TOKEN     # 引用下面的 GitHub secret 名
syncs:
  - name: myblog/TestFlightCN
    channel: TestFlightCN
    instance: myblog
    tag: TestFlightCN
    max_per_run: 10
    with_source_link: true
    keep: 100                        # 可选：每个频道最多保留 100 条
```

### 3. 配置 GitHub secrets

在仓库 **Settings → Secrets and variables → Actions** 添加每个实例的 access token，secret 名与 `config.yaml` 的 `token_env` 对应，例如 `ECH0_MYBLOG_TOKEN`。

然后在 [`.github/workflows/sync.yml`](./.github/workflows/sync.yml) 的 `Run relay` 步骤把 secret 映射进 `env:`（GitHub 无法按动态名索引 secret，每个实例加一行）：

```yaml
env:
  ECH0_MYBLOG_TOKEN: ${{ secrets.ECH0_MYBLOG_TOKEN }}
```

### 4. 发布二进制并开跑

```bash
git tag v0.1.0 && git push --tags     # 触发 build.yml，产出 release 二进制
```

到仓库 **Actions** 页手动 `Run workflow` 跑一次 `sync`，确认下载/运行/提交 state 正常，之后 cron 会每 30 分钟自动同步。

## 配置字段速查

| 字段 | 说明 |
|------|------|
| `instances.<name>.base_url` | 实例根地址（不含 `/api`） |
| `instances.<name>.token_env` | 持有 access token 的环境变量名（对应 GitHub secret） |
| `syncs[].name` | 稳定唯一的 state key，留空默认 `<instance>/<channel>` |
| `syncs[].channel` | 公开频道 slug，抓取 `t.me/s/<channel>` |
| `syncs[].instance` | 指向 `instances` 里的键 |
| `syncs[].tag` | 来源标签；`keep>0` 时**必填**且同实例内唯一 |
| `syncs[].max_per_run` | 每次运行最多发布几条（按最旧优先，积压跨轮排空，默认 10） |
| `syncs[].with_source_link` | 正文尾部追加 `🔗 https://t.me/<channel>/<id>` |
| `syncs[].upload_images` | `true` = 图片下载后上传到实例（localfs，waterfall 布局，需 `file:write`）；`false`（默认）= 嵌入 CDN 直链 |
| `syncs[].private` | 是否发为私密 |
| `syncs[].keep` | 保留上限；`0`/缺省 = 不清理 |
| `syncs[].max_delete_per_run` | 每轮删除数量上限（安全护栏，默认 50） |
| `syncs[].backfill_on_first_run` | 首次运行是否补历史（默认 `false`：只记录当前最新 id 作游标） |
| `syncs[].backfill_limit` | 补历史条数上限（仅 backfill 开启时生效，默认 20） |

## 本地调试

```bash
# 只抓取 + 渲染，不发帖 / 不删除 / 不写 state（无需 token）
go run ./cmd/relay -config config.yaml -dry-run -verbose

# 只跑某一个同步项
go run ./cmd/relay -config config.yaml -sync myblog/TestFlightCN -dry-run
```

跑测试：`go test ./...`

## 行为与取舍

- **首次运行**：默认只把游标播种到频道当前最新 id、**不补历史**，避免第一次刷屏。要补历史设 `backfill_on_first_run: true`。
- **图片**：默认以 markdown `![](链接)` 嵌入 Telegram CDN 图链，不下载，链接理论上可能失效。开启 `upload_images` 后改为下载并上传到 Ech0 本地存储、以 `echo_files` 挂载（waterfall 布局）；单张图片失败时自动降级为该图的 CDN 直链，不阻塞同步。发帖失败时已上传的图片会尽力清理（即使清理失败,Ech0 也会在 24 小时后回收未被引用的上传）。
- **cron 尽力而为**：GitHub 定时任务在高负载时会延迟数分钟；仓库 60 天无活动会自动停调度。
- **至少一次**：游标在每条成功后推进、运行结束保存。极端情况下（发帖成功但进程崩溃在保存前）下轮可能重发少量帖子。
- **公开频道限定**：只支持有公开网页预览（`t.me/s/<频道>`）的频道；私有/仅成员频道不支持。
- **数据永续建议**：目标实例建议定期用 Ech0 的快照导出做兜底备份。

## 许可证

[Apache-2.0](./LICENSE)
