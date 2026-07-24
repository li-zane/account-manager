# Account Manager Frontend

独立的 React + TypeScript + Vite 管理台，对接 `backend/` 下的 Go API。
邮箱管理首屏按平台筛选主邮箱，分裂地址和 Cloudflare 转发地址展开显示在
主邮箱下；主邮箱数量与子地址数量分别统计。工作区和设置使用独立导航，
常用平台可固定到导航栏。

界面采用 Kawaii Minimal 风格，但保留运维控制台需要的信息密度。桌面主体
使用可用页面宽度，详情、预览、备份等操作文本最低为 14px，常用表单控件
高度至少 44px。390px 移动端使用完整双列平台/设置导航，并保持页面级无
横向滚动。

## 已接入功能

- Microsoft、Gmail 与 Cloudflare 转发邮箱目录，主邮箱/分裂地址层级及数量。
- 邮箱名称点击复制，并保留行内复制按钮与反馈提示。
- 邮箱详情抽屉展示 client ID、共享 RT 状态、Graph/REST/IMAP 能力及各通道短期 AT 有效期；敏感 RT 通过明确的管理员揭示操作显示。
- 平台取件密钥自动签发、状态展示与按格式导出；服务端保留加密的管理员导出副本和独立的鉴权摘要。
- 收件箱弹窗支持 INBOX/垃圾箱、Graph/IMAP 手动探测、增量缓存、同步状态和本地全文搜索。
- 内置 legacy 格式、自定义分隔符/JSON/模板格式、导入预览、冲突策略及指定格式导出。
- 服务端令牌自动刷新开关与提前刷新分钟数，使用版本号处理并发更新。
- Cloudflare 连接配置，保存后仅展示脱敏连接状态。
- S3/WebDAV 备份目标新增、编辑、启停、手动执行、历史筛选，以及带确认步骤的异步恢复和状态轮询。

## 本地运行

```powershell
npm install
npm run dev
```

默认地址为 `http://127.0.0.1:5174`，API 请求通过 Vite 代理发送到
`http://127.0.0.1:8080`。可复制 `.env.example` 为 `.env.local` 并调整：

```text
VITE_BACKEND_ORIGIN=http://127.0.0.1:8080
VITE_API_TOKEN=TOKEN
VITE_USE_MOCKS=false
```

持久化后端启用 `ADMIN_API_TOKEN` 时，前端开发环境使用对应的
`VITE_API_TOKEN`。独立视觉预览可显式设置 `VITE_USE_MOCKS=true`；默认值
为真实 API，连接或校验错误直接显示在对应页面。

## 主要 API

- `GET /api/v1/mailboxes/overview`：邮箱目录、分裂地址与备份摘要。
- `GET /api/v1/mailboxes/:id/detail`：凭据摘要、别名和关联平台账号。
- `POST /api/v1/mailboxes/:id/credentials/reveal`：管理员按需读取上游凭据。
- `GET|PUT /api/v1/settings/token-refresh`：读取和保存 worker 刷新设置。
- `GET|PUT /api/v1/provider-connections/:provider`：读取和保存脱敏的平台连接配置。
- `GET|POST /api/v1/mailbox-formats`：读取或创建导入导出格式。
- `POST /api/v1/mailboxes/import/preview`、`POST /api/v1/mailboxes/import`：预览并提交批量导入。
- `POST /api/v1/mailboxes/export/preview`、`POST /api/v1/mailboxes/export`：预览并按选定格式导出。
- `GET|POST /api/v1/backups/targets`：读取或创建 S3/WebDAV 目标。
- `GET|PUT /api/v1/backups/targets/:id`：读取或按版本号更新目标。
- `GET|POST /api/v1/backups/runs`：读取历史或为指定目标排队备份。
- `POST /api/v1/backups/runs/:id/restore`：提交 `RESTORE` 确认并开始异步恢复。
- `GET /api/v1/backups/restores/:id`：轮询恢复状态。

编辑备份目标时，可保持现有脱敏连接配置，也可显式替换凭据。后端响应仅
返回非敏感位置摘要与凭据存在标志；恢复操作显示 `running`、`succeeded`
或 `failed` 状态。

## 验证

```powershell
npm run typecheck
npm run build
```

两项命令均通过。浏览器检查覆盖 390px、1440px 和 1920px 视口；390px
最小可见操作文字为 14px，页面无 body 级横向溢出，桌面主体随视口展开。

真实环境的 Outlook Graph、Outlook IMAP、Gmail IMAP、平台取件密钥及转发
邮件严格分裂地址过滤已有聚合通过记录。Cloudflare 新建真实路由仍属于部署
验收项，仓库不记录邮箱地址或连接密钥。
