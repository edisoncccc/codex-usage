# 公开 Fork 与双语 README 设计

**日期：** 2026-08-25<br>
**状态：** 已获用户批准，待实现<br>
**上游：** `zJay26/codex-usage`，基线提交 `cd6d4fdbff54838aed7e38a8bc4edf022c6ce8c7`

## 工作目标

将当前本地改版作为 `zJay26/codex-usage` 的公开 MIT Fork 发布，并把仓库首页整理成对中文和英文读者同等友好的成熟开源项目入口。README 要快速说明 Token 去向、缓存利用和 Standard API 等价成本，同时明确本地隐私边界、Fork 来源及当前只提供源码的发布状态。

## 已确认决策

- 使用 GitHub Fork 保留上游关联和完整提交历史，不包装成从零开发的原创项目。
- 中文使用 `README.md`，英文使用 `README.en.md`；两份文档结构、图片和信息层级保持一致，顶部互相切换。
- 采用“产品首页型”布局：静态主视觉负责第一印象，12 秒动态 Demo 放在下一屏解释功能。
- 继续使用仓库内已有的合成数据素材，不使用真实 Dashboard、Thread 标题、项目路径或用户数据截图。
- 保留 MIT `LICENSE` 和 `THIRD_PARTY_NOTICES.md`，在 README 显著说明上游来源和本 Fork 的修改范围。
- 第一阶段只发布源码，不创建或承诺尚不存在的预编译 Release，不发布未签名 EXE。

## README 信息架构

中文和英文 README 使用以下同构顺序：

1. **语言切换和 Fork 状态**
   - 中文页显示 `English` 链接，英文页显示 `简体中文` 链接。
   - 一句话说明这是基于 `zJay26/codex-usage` 的社区维护 Fork，并链接上游仓库与 MIT License。
2. **产品首屏**
   - 项目名称。
   - 一句短价值主张：在本机看清 Codex Token 用在哪里、缓存如何利用，以及按公开 API 价格折算的等价成本。
   - CI、Go、MIT、Local-first 等状态徽章；仓库相关徽章必须指向 Fork 自己的地址。
   - 使用 `Codex-Usage.png` 作为静态主视觉。
3. **动态演示**
   - 使用 `docs/media/codex-usage-demo.gif`。
   - 明确素材为合成数据，避免读者误认为包含真实用户信息。
4. **四项核心价值**
   - 本地与隐私。
   - Token、项目、Thread、Session 和 Agent 归属。
   - Cached Input、Cache Write 与缓存命中率。
   - Standard API 等价成本和定价覆盖率。
5. **从源码开始使用**
   - 给出 Go 版本、测试和构建命令。
   - 不展示指向上游 Release 的下载按钮，避免用户误以为下载的是本 Fork。
6. **能力与边界**
   - 保留现有的功能表、统计范围、隐私边界和“等价成本不是账单或配额”的说明。
7. **工作原理与技术细节**
   - 保留 JSONL、SQLite、增量扫描、去重、fork 识别和本地 HTTP Dashboard 的数据流说明。
8. **Fork 改进和上游致谢**
   - 只列出能由当前差异和测试证实的改进。
   - 链接上游仓库、许可证和第三方声明，不声称独占原项目成果。

## 视觉素材

| 素材 | 用途 | 约束 |
|---|---|---|
| `Codex-Usage.png` | README 第一屏静态主视觉 | 中英文共用，合成数据 |
| `docs/media/codex-usage-demo.gif` | 第二屏动态功能演示 | 保留合成数据说明 |
| `docs/images/dashboard.png` | 可折叠桌面截图 | 仅在文件存在且不含真实数据时使用 |
| `docs/images/dashboard-mobile.png` | 可折叠移动端截图 | 仅在文件存在且不含真实数据时使用 |

本次不生成新的品牌插画，不引入外部 CDN 图片，也不复制用户提供的真实截图。

## 发布方式

1. 在当前上游基线之上保留本地修改并建立可追踪提交。
2. 使用当前已认证的 GitHub 账号创建公开 Fork。
3. 将上游远程保留为 `upstream`，将个人 Fork 设置为 `origin`，优先使用 SSH 推送。
4. 运行完整测试和只读隐私扫描。
5. 将经过验证的提交推送到 Fork 的默认分支。
6. 不在本次创建 GitHub Release；二进制发布、代码签名和 SHA256 清单作为后续独立工作。

## 验证与完成标准

- `README.md` 与 `README.en.md` 顶部可以互相切换，主要章节一一对应。
- README 中不存在误导性的上游下载链接、无效的个人 Fork Release 链接或真实用户数据。
- 所有本地图片路径在 GitHub Markdown 中可解析。
- 原 `LICENSE` 与 `THIRD_PARTY_NOTICES.md` 保持存在。
- `go test ./...` 通过。
- Dashboard 自动化测试通过。
- Git 差异检查和隐私标记扫描没有阻断项。
- GitHub Fork 为公开仓库，默认分支包含经验证的 README 与本地功能改动。

## 会话执行记录

### 已执行

- 核对上游仓库、MIT License、第三方声明、远程地址和当前基线提交。
- 扫描受 Git 管理的文件，未发现真实用户名、本机路径、会话 ID 或凭据进入当前源码差异。
- 比较三种 README 视觉方向并确认采用“产品首页型”。
- 确认双语文档同等权重、互相切换，并只使用合成数据视觉素材。

### 计划修改文件

- `README.md`
- `README.en.md`
- 必要时仅增加发布过程文档；不改动与本次目标无关的业务代码。

### 计划运行命令

```text
go test ./...
npm test
git diff --check
gh auth status
ssh -T git@github.com
```

### 关键边界

- 不上传 `usage.sqlite`、Codex JSONL、构建目录、浏览器测试产物或本机配置。
- 不删除文件。
- 不创建或发布 EXE。
- GitHub 外部写入仅限用户明确授权的公开 Fork 创建与推送。

### 后续待办

- 用户审阅本设计文档。
- 编写并审阅实现计划。
- 修改中英文 README、补充会话交接记录、运行验证并发布 Fork。
