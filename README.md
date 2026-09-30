# Babyone Check

Go + Gin + SQLite 网站监测后端，供独立前端调用。单进程部署，数据库保存在本地磁盘；不要把同一数据库放在网络共享盘或启动多个调度实例。

## 运行

需要 Go 1.26+。纯 Go SQLite，无需 CGO。

```bash
go mod download
API_TOKEN='replace-with-a-long-random-token' go run .
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

最新结果超过两倍检测间隔加超时时间则状态 unknown。调度启动后立即检测，最多 8 个并发探测，每批完成后调度下一批；目标多时可能延迟，不保证硬实时。历史记录永久保存，后续可加入保留策略、日汇总、告警、连续失败阈值和多探测点。此版未实现事故管理或计划维护。

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
