# Babyone Check

Go + Gin + SQLite 网站监测后端，供独立前端调用。单进程部署，数据库保存在本地磁盘；不要把同一数据库放在网络共享盘或启动多个调度实例。

## 运行

需要 Go 1.26+。纯 Go SQLite，无需 CGO。

```bash
go mod download
cp .env.example .env
# 编辑 .env 后启动
go run .
```

默认监听 `127.0.0.1:8080`，数据库 `monitor.db`。环境变量：

- `API_TOKEN`：必填，所有 `/api/v1` 接口使用 `Authorization: Bearer <token>`。
- `DB_PATH`：数据库路径，需要目录已存在且可写。
- `LISTEN_ADDR`：监听地址。
- `CORS_ORIGIN`：可选，允许的前端 Origin，精确匹配。
启动时自动初始化内置项目及六个检测目标，重复启动不会重复创建，也不会覆盖已暂停目标的配置。无需设置初始化参数。

初始 HTTP 目标：babycare、music、yolo 的 `https://baby.hanlinbo.cn/{service}/api/health`，每 60 秒检测，10 秒超时，HTTP 200 为成功，不跟随重定向。
初始 SSL 目标：`www.hanlinbo.cn`、`www.hanxiaoxin.cn`、`baby.hanlinbo.cn`，每天检测，443 端口，验证信任链、域名及有效期。域名按用户输入缺失分隔符的情况推定，请确认。校验失败时尝试读取证书有效期供诊断，但不将检测标为成功。

## API v1

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/health` | 本服务和数据库健康，无需认证 |
| GET/POST | `/api/v1/projects` | 列表/创建项目 |
| GET/POST | `/api/v1/projects/:project/targets` | 列表/创建检测目标 |
| PATCH | `/api/v1/targets/:target` | `{ "enabled": false }` 暂停/恢复 |
| GET | `/api/v1/status` | 所有项目的目标状态，支持分页 |
| GET | `/api/v1/projects/:project/status` | 指定项目下所有目标状态，支持分页 |
| GET | `/api/v1/targets/:target/history` | 分页原始记录 |
| GET | `/api/v1/targets/:target/stats` | 可用率、平均延迟及时间桶统计 |

创建项目：`{"name":"my-project"}`。
创建目标：

```json
{"name":"api","kind":"http","address":"https://example.com/api/health","interval_seconds":60,"timeout_seconds":10,"expected_status":200,"enabled":true}
```

SSL 使用 `kind: "ssl"`、`address: "example.com"`、`interval_seconds: 86400`。
所有时间戳为 UTC Unix 秒。历史和统计支持 `from`（包含）、`to`（不包含），默认过去 24 小时，最大 366 天。
历史支持 `limit`（默认 100，最大 1000）、`before_id`，使用返回的 `next_before_id` 翻页。
统计支持 `bucket_seconds`（默认 3600，60..86400），返回 UTC 对齐的非空时间桶，空桶由前端显示为无数据。

```bash
curl -H 'Authorization: Bearer your-token' http://127.0.0.1:8080/api/v1/projects/1/status
curl -H 'Authorization: Bearer your-token' 'http://127.0.0.1:8080/api/v1/targets/1/stats?bucket_seconds=3600'
```

## 统计口径与边界

`availability_percent = successful_samples / total_samples * 100`，没有样本返回 null。SSL 的成功率表示 TLS 校验成功率，不是业务服务可用率。暂停/调度停机没有样本，不自动算成功或失败；这是样本可用率，不是按实际故障持续时间计算的 SLA。

最新结果超过两倍检测间隔加超时时间则状态 unknown。调度启动后立即检测，最多 8 个并发探测，每批完成后调度下一批；目标多时可能延迟，不保证硬实时。历史记录永久保存，后续可加入保留策略、日汇总、连续失败阈值和多探测点。此版未实现事故管理或计划维护。

所有管理和读取 API 共用一个服务端 token，不要把管理 token 写入公开前端 JS。建议前端后端代理，或后续增加只读公开状态 API 和独立写权限。检测目标由可信管理员维护，可访问内网，不能开放给不可信用户任意添加 URL。跨域 Origin 不是认证。

生产部署建议放在被监测服务器之外，通过 HTTPS 反向代理访问，设置进程自动重启，备份 SQLite（用 SQLite backup 或停服后复制），保持机器时间同步。监测与业务同机时，机器宕机会同时停止监测。

## 验证和构建

```bash
go test ./...
go vet ./...
make amd64
make arm64
make armv7
```

仓库原有 go.mod 的 Go 1.27 改为 1.26；工具链验证使用 Go 1.26.1。项目结构：`main.go` 启动与优雅退出；`internal/monitor/store.go` 存储；`check.go` 探测与调度；`api.go` HTTP 路由与统计。

## 状态列表分页

`GET /api/v1/status?page=1&page_size=20` 查询所有项目下的检测目标。
`GET /api/v1/projects/1/status?page=1&page_size=20` 查询指定项目下的检测目标。
两者都按目标 ID 升序，默认 page=1、page_size=20，每页最大 100。返回 `data`、`page`、`page_size`、`total`、`total_pages`，每项保留 `target`（含 project_id）、`state`、`latest` 及证书预警字段。超出最后一页返回空数组；非法分页参数返回 400。分页单位是检测目标。

## .env 和邮件通知

```bash
cp .env.example .env
# 编辑 .env：至少修改 API_TOKEN，邮件需要填写 SMTP_*。
go run .
```

Go 程序启动时通过 godotenv 加载当前工作目录的 `.env`；操作系统环境变量优先。`.env.example` 是模板，不自动加载；真实 `.env` 已被 Git 忽略，不要提交密码或授权码。修改 `.env` 后重启服务。

配置分为两层：启动配置（API_TOKEN、数据库、监听、SMTP 密钥）来自环境变量/`.env`；运行配置（自动通知开关）保存在 SQLite，服务重启后保留，API 不返回 SMTP 密码或 Token。

SMTP 支持 587 STARTTLS 和 465 隐式 TLS，必须验证服务器证书。`SMTP_TO` 支持逗号分隔的多个邮箱。某些提供商需要 SMTP 授权码而不是登录密码。未配置 SMTP 时服务可以正常启动，但无法开启通知。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/v1/settings` | 返回 auto_notify、smtp_configured、ssl_warning_days |
| PATCH | `/api/v1/settings` | 持久化修改自动通知开关 |

```bash
curl -H 'Authorization: Bearer your-token' http://127.0.0.1:8080/api/v1/settings
curl -X PATCH -H 'Authorization: Bearer your-token' -H 'Content-Type: application/json' \
  -d '{"auto_notify":true}' http://127.0.0.1:8080/api/v1/settings
```

`AUTO_NOTIFY=false` 是**新数据库的初始值**；后续以数据库保存的设置为准，避免重启覆盖前端设置。已开启通知的数据库若缺少有效 SMTP 配置，启动会报错，防止误以为通知正常。

通知规则：首次发现不可用时告警；恢复正常时发送恢复通知；证书剩余不足 30 天时预警，同一证书只提醒一次，证书更换后重新判定。首次正常检测不发邮件。相同故障状态不会在每次检测时重复发送。状态和发送尝试存在 SQLite，重启后仍可去重。发送失败不更新已通知状态，下次检测重试；SMTP 超时 10 秒。通知关闭期间不发送；重新开启后在下次检测评估当前状态。

通知发送与检测运行在同一进程，串行发送，未实现独立消息队列、限流、每日提醒或通知日志查询 API。邮件依赖 SMTP 接受，不能保证最终进收件箱；在 SMTP 接受但进程未保存状态时崩溃，可能重复通知。此版健康判断仍基于 HTTP 状态码。

## 应用补丁

本次补丁仅包含邮件通知及服务配置的增量修改，基于最新远端提交 `ca15583b6851098a523ec204f0d8560c1bf5e6c9`（feat: add monit）。请在干净的项目根目录使用。

```bash
git pull --ff-only
git status --short
git apply --check /path/to/babyone_check.patch
git apply /path/to/babyone_check.patch
go mod download
go test ./...
cp .env.example .env
# 填写 .env 后启动。
go run .
```

`git apply` 一次应用所有文件，不会自动提交。确认修改后执行 `git add .`、`git commit`、`git push`。Windows 下可在 Git Bash 或 PowerShell 中执行，将路径替换为实际补丁路径。如果 check 提示冲突，保留本地修改并提供最新仓库或差异，以便生成基于当前版本的增量补丁；不要忽略错误或强制覆盖。

## URL Token 认证

所有 `/api/v1` 接口现在也支持查询参数 `token`，无需额外开关：

```text
http://127.0.0.1:8080/api/v1/status?token=your-token&page=1&page_size=20
http://127.0.0.1:8080/api/v1/settings?token=your-token
```

Token 与 `.env` 的 `API_TOKEN` 相同。原有 `Authorization: Bearer ...` 仍支持；两者同时提供时以请求头为准，错误请求头不会回退到 URL Token。重复 token 查询参数不作为有效凭据。应用在日志及错误处理中移除 URL token，但浏览器历史和上游代理仍可能记录原始 URL，外网访问请使用 HTTPS。默认 `.env.example` 使用 `CORS_ORIGIN=*` 允许任意来源。

本次 `babyone_check_token.patch` 基于远端提交 `cd42608`（feat: add config），仅增加 URL Token 及补齐通配跨域支持。在项目根目录执行：

```bash
git pull --ff-only
git apply --check /path/to/babyone_check_token.patch
git apply /path/to/babyone_check_token.patch
go test ./...
```
