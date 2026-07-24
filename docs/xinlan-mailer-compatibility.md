# Outlook 取件能力对照

本项目参考心蓝邮箱助手公开产品页和帮助文档中的邮箱管理能力，保持自身的
provider-neutral 接口边界，不复制其桌面软件的数据模型。

参考资料：

- <https://www.bhdata.com/soft/22372356.html>
- <https://docs.bhdata.com/bhmailer/>
- <https://docs.bhdata.com/bhmailer/ms-plugin.html>

## 当前覆盖

| 能力 | Account Manager 实现 |
| --- | --- |
| 批量导入与导出 | 预览式批量导入、冲突策略、Outlook 四段格式和自定义格式 |
| Microsoft OAuth 取件 | 一个共享 RT，Graph/REST 与 IMAP 双通道 |
| 凭据和能力检查 | 详情展示 RT 状态、通道配置/验证状态和通道 AT 时间 |
| 邮件目录 | 收件箱与垃圾箱快速切换 |
| 本地邮件存储 | PostgreSQL 缓存、去重和增量游标 |
| 定时与手动收信 | 可配置自动探测，以及按文件夹和通道手动同步 |
| 邮件检索 | 发件人、主题、收件人和正文的本地搜索 |
| 分裂邮箱 | 作为主邮箱子项展示，并按精确收件人隔离缓存和查询 |
| 外部取件 | 平台邮箱 ID 与本站取件密钥，不暴露上游 RT |
| HTTP API | provider-neutral 管理、缓存读取和同步接口 |

## 语义约束

- “双通道”表示同一个 RT 可服务 Graph/REST 和 IMAP，不表示存在两份 RT。
- Microsoft RT 通常没有可直接读取的固定到期日。界面显示 RT 状态为
  “无固定到期日”，Graph/IMAP 时间只代表短期 AT。
- 已配置表示凭据具备该通道的必要字段；已验证需要通道级验证记录。过往
  聚合测试结果不会被伪装成每个邮箱都验证成功。
- 自动 RT 刷新关闭时，后台任务、手动取件和 401 重试都不会刷新 RT。

## 后续账号管理范围

修改密码、辅助邮箱、账号解锁和 2FA 属于未来的 Microsoft 账号管理模块，
不会耦合进邮箱取件凭据模型。
