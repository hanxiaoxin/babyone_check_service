# Babyone Check

Go + Gin + SQLite 的 HTTP / SSL 监测服务，原生 ES Modules 前端，无需 npm 或编译前端。

## 运行

需要 Go 1.26+，无需 CGO。

```bash
cp .env.example .env
# 设置 API_TOKEN 和其他启动参数
go run .
```

访问 `http://服务器IP:8081/ui/`，支持 `?token=你的Token`。默认监听 `0.0.0.0:8081`，系统环境变量优先于 `.env`。`API_AUTH_ENABLED=false` 可关闭网页和 API 的 token 认证，修改启动配置后重启。

**本版使用全新数据结构，不兼容旧数据库。** 默认数据库 `monitor-v2.db`。已有 `.env` 需要把 `DB_PATH` 改为 `monitor-v2.db` 或另一个新路径；旧数据库不会被自动删除或迁移。首次启动创建六个内置监测服务，删除后不会因重启重新出现。

## 页面

- 检测总览：每个服务一行，最近 30 个 UTC 日期的历史色块、样本可用率 / TLS 校验成功率、检测间隔、最近检测和下次检测倒计时。灰色代表无样本；色块点击打开详情。
- 项目管理：项目就是监测服务，列表支持创建、编辑、暂停 / 恢复、删除；配置均在弹窗完成。检测间隔和超时可编辑，保存后重新安排检测。删除会停止检测并保留已产生的记录。
- 历史与统计：24 小时 / 7 天 / 30 天及自定义时间范围，统计趋势与原始记录分开，支持继续加载记录。弹窗标题与底栏固定，Esc 或点击外部关闭。
- 通知设置：配置 SMTP、自动通知、证书提前提醒天数（默认 1 天，可设 1–365）。保存后立即生效。测试邮件使用已保存配置，不依赖自动通知开关，30 秒内只允许一次。

通知支持故障、恢复和证书到期预警，提供 HTML 与纯文本邮件。SMTP 支持 STARTTLS / TLS，验证服务器证书；发送成功表示 SMTP 接受，不保证最终到达收件箱。密码不回显，留空保留；数据库包含 SMTP 密码，应保护数据库和备份。

证书展示等级与邮件通知天数独立：超过 30 天正常、30 天内提醒、7 天内警告、1 天内紧急、已过期标红。调度时间来自后台，最多 8 个并发检测；检测超时或排队时显示相应状态。重启后立即重新安排检测。可用率按检测样本计算，无数据不计成功；SSL 成功率表示 TLS 校验，非业务可用率。

## API

`/api/v1` 默认使用 Bearer Token 或 URL token。关闭认证后任何能访问服务的人都能修改配置。外网访问使用 HTTPS。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET/POST | `/api/v1/monitors` | 查询 / 创建服务 |
| PATCH/DELETE | `/api/v1/monitors/:id` | 编辑 / 删除服务 |
| GET | `/api/v1/status` | 状态、汇总和调度时间，支持 page、page_size、kind |
| GET | `/api/v1/monitors/timeline?ids=1,2` | 批量 30 天每日统计 |
| GET | `/api/v1/targets/:id/history` | 历史记录，limit / before_id 分页 |
| GET | `/api/v1/targets/:id/stats` | 样本统计和时间桶 |
| GET/PATCH | `/api/v1/settings` | SMTP、通知开关和提前天数 |
| POST | `/api/v1/settings/test-mail` | 发送测试邮件 |

历史和统计支持 Unix 秒 from / to，最长 366 天。创建 / 编辑服务字段：name、description、kind（http / ssl）、address、interval_seconds、timeout_seconds、expected_status、enabled。SSL 地址仅填域名，HTTP 使用完整 URL。名称最多 100 UTF-8 字节，描述最多 2000 字节。

```bash
go test ./...
go vet ./...
make armv7
```

补丁固定为 `babyone_check.patch`：`git apply --check babyone_check.patch`，确认后 `git apply babyone_check.patch`，重新编译并重启服务。
