# Babyone Check

Go + Gin + SQLite 服务可用性与 SSL 证书监测，内置无需编译的原生 ES Modules 前端。

## 运行

需要 Go 1.26+，无需 CGO。

```bash
cp .env.example .env
# 至少设置 API_TOKEN。
go run .
```

访问 `http://服务器IP:8080/ui/`，输入 API Token；也支持 `/ui/?token=你的Token`。页面连接后移除地址栏中的 token，仅在内存中保存，刷新需要重新输入。前端资源通过 embed 打包进 Go 二进制，无需 npm 或额外部署。

默认监听 `0.0.0.0:8080`，数据库为 `monitor.db`。`.env` 可配置 `LISTEN_ADDR`、`DB_PATH`、`API_TOKEN`、`CORS_ORIGIN`（`*` 允许所有来源）；修改后重启。SMTP 与通知开关在网页“通知设置”中配置，保存在 SQLite，保存后立即生效。密码不回显，留空保留，勾选清除可删除；多个收件人用逗号分隔。数据库包含 SMTP 密码，请保护数据库及备份文件。旧 .env 中的 SMTP_* 不再读取，升级后需在网页重新配置；未配置时自动通知关闭。

页面支持项目描述、HTTP/SSL 筛选、状态分页、创建项目和检测目标、暂停/恢复、历史分页、时间范围统计及通知开关。启动时自动创建内置项目与检测目标。单进程使用本地 SQLite。

## API

`/api/v1` 支持 `Authorization: Bearer <token>` 或 `?token=<token>`，同时传递以请求头为准。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/health` | 服务健康，无需认证 |
| GET/POST | `/api/v1/projects` | 列出/创建项目，含 description |
| PATCH | `/api/v1/projects/:id` | 更新 description |
| GET/POST | `/api/v1/projects/:id/targets` | 列出/创建目标 |
| PATCH | `/api/v1/targets/:id` | 设置 enabled |
| GET | `/api/v1/status`、`/api/v1/projects/:id/status` | 状态分页 |
| GET | `/api/v1/targets/:id/history` | 检测历史 |
| GET | `/api/v1/targets/:id/stats` | 样本成功率及平均延迟 |
| GET/PATCH | `/api/v1/settings` | 查询/保存 SMTP 与通知开关 |

目标及状态列表支持 `kind=http/ssl`，不传查询全部。状态分页使用 `page`、`page_size`（最大 100）。历史和统计使用 Unix 秒 `from`、`to`，默认 24 小时、最大 366 天；历史使用 `limit` 与返回的 `next_before_id` 作为下一次 `before_id`。

HTTP 地址使用完整 URL，SSL 地址仅填域名。项目描述最多 2000 UTF-8 字节。可用率按成功样本/总样本计算，无样本返回 null；SSL 成功率表示 TLS 校验结果。通知涵盖故障、恢复及证书不足 30 天。

外网使用 HTTPS；URL token 可能被浏览器历史和上游代理记录。管理接口共用 token，仅供可信管理员使用。

## 验证与构建

```bash
go test ./...
go vet ./...
make amd64
```

补丁文件名固定为 `babyone_check.patch`，在项目根目录应用：

```bash
git apply --check babyone_check.patch
git apply babyone_check.patch
```
