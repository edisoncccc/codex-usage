package cliui

import (
	"fmt"
	"strings"
)

type Locale string

const (
	Chinese Locale = "zh-CN"
	English Locale = "en"
)

var messages = map[Locale]map[string]string{
	Chinese: {
		"error.prefix":           "错误:",
		"error.invalidLang":      "不支持语言 %q；可选值为 en 或 zh-CN",
		"error.langMissing":      "--lang 需要一个值；可选值为 en 或 zh-CN",
		"error.unknownCommand":   "未知命令 %q",
		"error.invalidArguments": "参数无效：%v",
		"error.buildMetadata":    "构建元数据 BuildDirty=%q 无效",
		"usage": `Codex Usage — 当前电脑的 Codex Token 本地统计

用法:
  codex-usage [--lang en|zh-CN]       打开本机 Dashboard
  codex-usage open                    打开本机 Dashboard
  codex-usage install [--yes] [--json] [--skip-scan] 用户级安装、初始化 JSONL 扫描并启动
  codex-usage summary --since 7d      命令行摘要（支持 --json / --csv）
  codex-usage scan [--rebuild] [--json] 增量扫描本机 Codex session
  codex-usage doctor                  检查路径、JSONL 数据源与服务状态
  codex-usage config add-home PATH    添加一个额外 CODEX_HOME
  codex-usage update --check|--yes [--json] 显式检查或执行更新
  codex-usage uninstall [--purge] [--yes] [--json] 卸载；默认保留统计库与配置
  codex-usage serve                   前台运行本地服务
  codex-usage version [--json]        显示版本或输出机器可读构建身份

语言:
  --lang 优先于 CODEX_USAGE_LANG，其次跟随系统语言；支持 en、zh-CN。

边界:
  “电脑”是运行 Codex 客户端和 codex-usage 的主机；不是 shell/tool 实际执行的远程环境。
  不读取 auth.json、prompt、回复、reasoning 或工具输出；不读取真实账单或账号配额，
  仅按本机 Token 与内置 Standard API 价格提供等价成本估算。`,
		"serve.running": "Codex Usage 正在 %s 运行；按 Ctrl+C 停止。\n",
		"open.start":    "启动本地服务: %w",
		"open.notReady": "本地服务未在 12 秒内就绪；请运行 codex-usage doctor",
		"open.opened":   "已打开",
		"open.noGUI":    "无图形环境时，在你的电脑运行：ssh -N -L %d:127.0.0.1:%d %s@%s\n",
		"flag.rebuild":  "清空派生统计后从全部 JSONL 重建",
		"flag.json":     "输出 JSON",
		"flag.csv":      "输出 CSV",
		"flag.since":    "范围：7d、30d、today、all 或 RFC3339",
		"flag.skipScan": "跳过首次历史扫描",
		"flag.yes":      "确认已获得用户授权并执行",
		"flag.purge":    "同时删除统计库和工具配置",

		"flag.updateCheck":                  "只读检查可用更新",
		"update.disabled":                   "当前仓库仅发布源码（source-only），可信二进制更新渠道尚未启用；请阅读 INSTALL.md。不会联网、下载、修改文件或自动降级为源码构建。",
		"uninstall.confirm.required":        "需要显式确认；请检查卸载路径后使用 --yes",
		"uninstall.confirm.declined":        "未获得卸载确认",
		"uninstall.confirm.title":           "卸载前确认",
		"uninstall.confirm.installPath":     "程序路径: %s",
		"uninstall.confirm.statePath":       "状态目录: %s",
		"uninstall.confirm.preserve":        "将保留统计数据库和配置: %s",
		"uninstall.confirm.purge":           "将永久删除此规范状态目录中的统计数据库和配置: %s",
		"uninstall.confirm.prompt":          "是否继续？输入 yes 或 是: ",
		"uninstall.complete.preserved":      "卸载已完成；统计数据库和配置保留在: %s",
		"uninstall.complete.purged":         "卸载已完成；程序、统计数据库和配置已删除。",
		"uninstall.complete.scheduled":      "卸载已安排；程序将在当前进程退出后删除，统计数据库和配置保留在: %s",
		"uninstall.complete.purgeScheduled": "卸载已安排；程序和状态目录将在当前进程退出后删除: %s",

		"summary.conflict":       "--json 与 --csv 不能同时使用",
		"summary.title":          "Codex Usage · 当前电脑 · %s\n",
		"summary.cached":         "  Cached Input    %s  (Input 的子集)\n",
		"summary.reasoning":      "  Reasoning       %s  (Output 的子集)\n",
		"summary.unattributed":   "历史未归属        %s  (只属于累计，不属于所选日期)\n",
		"scan.progress":          "扫描进度：Home %d/%d，发现 %d 个文件，处理 %d 个文件，处理 %d 条记录，新增 %d 个事件，%d 条提示。\n",
		"scan.complete":          "扫描完成：%d 个 Home，发现 %d 个文件，处理 %d 条记录，新增 %d 个事件，忽略 %d 个重复，%d 条提示，耗时 %.2fs\n",
		"scan.unattributed":      "有 %d 个 session 的差额仅计入“历史未归属”，未伪造每日分布。\n",
		"install.stopOld":        "停止旧版后台服务: %w",
		"install.stopCurrent":    "停止现有后台服务: %w",
		"install.migrateOld":     "迁移旧版本机数据: %w",
		"install.dbConflict":     "检测到两个并行统计库；为避免覆盖历史数据，升级已停止，请先备份并处理旧版状态目录",
		"install.migrated":       "已迁移旧版本机配置；如解析规则需要重建，将保留现有统计并等待用户确认。",
		"install.oldState":       "提示：旧版状态目录仍有未识别文件，未自动删除:",
		"install.permissions":    "警告：无法进一步收紧状态目录权限:",
		"install.installed":      "已安装:",
		"install.scanning":       "正在执行首次本地历史扫描（只解析 metadata 与 token_count）…",
		"install.scanWarning":    "警告：首次扫描未完成:",
		"install.scanDone":       "首次扫描：%d 文件，新增 %d 事件，%d 条提示。\n",
		"install.legacyRemoved":  "已从 %s 移除旧版 codex-usage 管理的 OTel exporter；其他配置未改动。",
		"install.health":         "后台服务未在 30 秒内通过本机 health check",
		"install.service":        "后台服务:",
		"install.warning":        "警告:",
		"install.cleanup":        "警告：旧版可执行文件未能自动清理:",
		"install.done":           "安装完成。后台服务会定期增量扫描本机 JSONL；无需重启 Codex。",
		"install.confirm.source": "安装来源: %s",

		"install.confirm.required":    "需要显式确认；请检查预检路径后使用 --yes",
		"install.confirm.declined":    "未获得安装确认",
		"install.confirm.title":       "安装前确认",
		"install.confirm.repository":  "规范仓库: %s",
		"install.confirm.installPath": "安装目录: %s",
		"install.confirm.statePath":   "状态目录: %s",
		"install.confirm.service":     "用户服务（当前用户）: %s",
		"install.confirm.loopback":    "本机回环地址: %s",
		"install.confirm.scanScope":   "扫描范围: %s",
		"install.confirm.preserve":    "默认卸载保留数据库和配置；只有 --purge 才清除数据。",
		"install.confirm.prompt":      "是否继续？输入 yes 或 是: ",
		"install.source.sourceBuild":  "源码构建 (source_build)",
		"install.phase.preflight":     "预检",
		"install.phase.stop_service":  "停止旧服务",
		"install.phase.install":       "安装或升级",
		"install.phase.scan":          "扫描历史",
		"install.phase.start_service": "启动服务",
		"install.phase.health_check":  "健康检查",
		"install.phase.complete":      "安装完成。",
		"install.progress.scan":       "扫描历史：发现 %d 个文件，处理 %d 个文件，新增 %d 个事件，%d 条提示。\n",

		"doctor.loopbackOK":      "%s:%d；不会监听公网",
		"doctor.loopbackError":   "配置了非 loopback 地址",
		"doctor.permissions":     "状态目录已限制为当前用户访问",
		"doctor.machineHost":     "数据库最初属于主机 %s，当前主机为 %s；可能复制/同步了 CODEX_USAGE_HOME，逐电脑边界不再可靠",
		"doctor.machinePlatform": "数据库记录平台为 %s/%s，当前为 %s/%s；请勿跨电脑同步 CODEX_USAGE_HOME",
		"doctor.database":        "%s；events=%d sessions=%d",
		"doctor.jsonlOnly":       "JSONL 是唯一 Token 计数来源；状态库仅用于发现文件与补充 metadata",
		"doctor.coverage":        "%d 条异常/覆盖提示，请在 Dashboard 查看",
		"doctor.homeUnreadable":  "%s 不存在或不可读",
		"doctor.stateWarning":    "%s：%s；扫描 %d 个 JSONL",
		"doctor.stateDB":         "%s：%d 个 canonical rollout",
		"doctor.stateMissing":    "%s 没有可识别状态库；扫描 %d 个 JSONL",
		"doctor.sharedHistory":   "session %s 同时出现在 %s 与 %s。若多台电脑同步同一 CODEX_HOME，安装前历史无法可靠拆分；统计会按 session 去重。",
		"doctor.legacyManaged":   "%s 仍含旧版 codex-usage OTel managed stanza；运行 install 可只移除该段",
		"doctor.configUntouched": "%s 无需 codex-usage 采集配置；第三方配置不会被读取或改写",
		"doctor.sharedHome":      "%s 位于常见跨系统挂载路径；确认它不是与 Windows 共用的 CODEX_HOME",
		"doctor.serviceOK":       "本地 Dashboard 可访问",
		"doctor.serviceDown":     "本地服务未运行",
		"doctor.identityOK":      "本地 Dashboard 身份匹配：%s %s (%s/%s)",
		"doctor.identityError":   "本地 Dashboard 身份验证失败：%v",
		"doctor.failed":          "健康检查发现错误",
		"doctor.n.stateDir":      "状态目录",
		"doctor.n.config":        "配置",
		"doctor.n.codexHome":     "Codex Home",
		"doctor.n.loopback":      "本机监听",
		"doctor.n.database":      "数据库",
		"doctor.n.machine":       "机器",
		"doctor.n.machineID":     "机器身份",
		"doctor.n.accounting":    "计费来源",
		"doctor.n.coverage":      "覆盖情况",
		"doctor.n.stateDB":       "状态索引",
		"doctor.n.history":       "共享历史",
		"doctor.n.codexConfig":   "Codex 配置",
		"doctor.n.sharedHome":    "共享 Home",
		"doctor.n.serviceID":     "服务身份",
		"doctor.n.privacy":       "隐私结构",
		"doctor.n.network":       "网络",
		"doctor.privacy":         "数据库 schema 仅含计数、时间、模型、来源、路径和标题；无 prompt/reply/reasoning/auth 字段",
		"doctor.network":         "运行时无外部上报客户端；Dashboard 仅监听 loopback",
	},
	English: {
		"error.prefix":           "Error:",
		"error.invalidLang":      "unsupported language %q; use en or zh-CN",
		"error.langMissing":      "--lang requires a value; use en or zh-CN",
		"error.unknownCommand":   "unknown command %q",
		"error.invalidArguments": "invalid arguments: %v",
		"error.buildMetadata":    "invalid BuildDirty build metadata value %q",
		"usage": `Codex Usage — local Codex token accounting for this machine

Usage:
  codex-usage [--lang en|zh-CN]       Open the local Dashboard
  codex-usage open                    Open the local Dashboard
  codex-usage install [--yes] [--json] [--skip-scan] Install for this user, initialize JSONL scanning, and start
  codex-usage summary --since 7d      Print a CLI summary (supports --json / --csv)
  codex-usage scan [--rebuild] [--json] Incrementally scan local Codex sessions
  codex-usage doctor                  Check paths, JSONL sources, and service state
  codex-usage config add-home PATH    Add another CODEX_HOME
  codex-usage update --check|--yes [--json] Explicitly check for or apply an update
  codex-usage uninstall [--purge] [--yes] [--json] Uninstall; keep the usage database and config by default
  codex-usage serve                   Run the local service in the foreground
  codex-usage version [--json]        Print the version or machine-readable build identity

Language:
  --lang overrides CODEX_USAGE_LANG, then the system locale; supports en and zh-CN.

Boundary:
  “Machine” means the host running the Codex client and codex-usage, not a remote shell/tool target.
  Codex Usage never reads auth.json, prompts, replies, reasoning, or tool output. It does not read
  bills or account quotas; cost is only a Standard API-equivalent estimate of local token usage.`,
		"serve.running": "Codex Usage is running at %s; press Ctrl+C to stop.\n",
		"open.start":    "start local service: %w",
		"open.notReady": "the local service was not ready within 12 seconds; run codex-usage doctor",
		"open.opened":   "Opened",
		"open.noGUI":    "With no graphical session, run this on your computer: ssh -N -L %d:127.0.0.1:%d %s@%s\n",
		"flag.rebuild":  "Clear derived accounting and rebuild it from all JSONL files",
		"flag.json":     "Output JSON",
		"flag.csv":      "Output CSV",
		"flag.since":    "Range: 7d, 30d, today, all, or RFC3339",
		"flag.skipScan": "Skip the first historical scan",
		"flag.yes":      "Confirm user authorization and proceed",
		"flag.purge":    "Also delete the usage database and tool configuration",

		"flag.updateCheck":                  "Only check for an available update",
		"update.disabled":                   "This repository is source-only; the trusted binary update channel is not enabled. Read INSTALL.md. No network request, download, file modification, or automatic source-build fallback was attempted.",
		"uninstall.confirm.required":        "explicit confirmation is required; review the uninstall paths and use --yes",
		"uninstall.confirm.declined":        "uninstall was not confirmed",
		"uninstall.confirm.title":           "Uninstall confirmation",
		"uninstall.confirm.installPath":     "Program path: %s",
		"uninstall.confirm.statePath":       "State directory: %s",
		"uninstall.confirm.preserve":        "The usage database and configuration will be preserved at: %s",
		"uninstall.confirm.purge":           "The usage database and configuration in this canonical state directory will be permanently deleted: %s",
		"uninstall.confirm.prompt":          "Proceed? Enter yes: ",
		"uninstall.complete.preserved":      "Uninstall complete; the usage database and configuration remain at: %s",
		"uninstall.complete.purged":         "Uninstall complete; the program, usage database, and configuration were removed.",
		"uninstall.complete.scheduled":      "Uninstall scheduled; the program will be removed after this process exits. The usage database and configuration remain at: %s",
		"uninstall.complete.purgeScheduled": "Uninstall scheduled; the program and state directory will be removed after this process exits: %s",

		"summary.conflict":       "--json and --csv cannot be used together",
		"summary.title":          "Codex Usage · this machine · %s\n",
		"summary.cached":         "  Cached Input    %s  (subset of Input)\n",
		"summary.reasoning":      "  Reasoning       %s  (subset of Output)\n",
		"summary.unattributed":   "Historical only   %s  (part of all-time totals, not the selected dates)\n",
		"scan.progress":          "Scan progress: Homes %d/%d, %d files discovered, %d files processed, %d records processed, %d events added, %d notices.\n",
		"scan.complete":          "Scan complete: %d Homes, %d files discovered, %d records processed, %d events added, %d duplicates ignored, %d notices, %.2fs\n",
		"scan.unattributed":      "%d sessions have deltas counted only as historical unattributed usage; no daily distribution was invented.\n",
		"install.stopOld":        "stop previous background service: %w",
		"install.stopCurrent":    "stop the current background service: %w",
		"install.migrateOld":     "migrate previous local data: %w",
		"install.dbConflict":     "two parallel usage databases were found; upgrade stopped to avoid overwriting history. Back up and resolve the previous state directory first",
		"install.migrated":       "Migrated the previous local configuration; if parser changes require a rebuild, existing statistics are preserved until the user approves it.",
		"install.oldState":       "Note: unrecognized files remain in the previous state directory and were not deleted:",
		"install.permissions":    "Warning: could not further restrict the state directory:",
		"install.installed":      "Installed:",
		"install.scanning":       "Running the first local history scan (metadata and token_count only)…",
		"install.scanWarning":    "Warning: first scan did not complete:",
		"install.scanDone":       "First scan: %d files, %d events added, %d notices.\n",
		"install.legacyRemoved":  "Removed the legacy codex-usage managed OTel exporter from %s; all other configuration was left unchanged.",
		"install.health":         "the background service failed its local health check within 30 seconds",
		"install.service":        "Background service:",
		"install.warning":        "Warning:",
		"install.cleanup":        "Warning: the previous executable could not be removed automatically:",
		"install.done":           "Installation complete. The background service incrementally scans local JSONL files; Codex does not need to restart.",
		"install.confirm.source": "Install source: %s",

		"install.confirm.required":    "explicit confirmation is required; review the preflight paths and use --yes",
		"install.confirm.declined":    "installation was not confirmed",
		"install.confirm.title":       "Installation confirmation",
		"install.confirm.repository":  "Canonical repository: %s",
		"install.confirm.installPath": "Install directory: %s",
		"install.confirm.statePath":   "State directory: %s",
		"install.confirm.service":     "Service (current user): %s",
		"install.confirm.loopback":    "Loopback endpoint: %s",
		"install.confirm.scanScope":   "Scan scope: %s",
		"install.confirm.preserve":    "Default uninstall keeps the database and configuration; only --purge removes data.",
		"install.confirm.prompt":      "Continue? Enter yes: ",
		"install.source.sourceBuild":  "source build (source_build)",
		"install.phase.preflight":     "Preflight",
		"install.phase.stop_service":  "Stop previous service",
		"install.phase.install":       "Install or upgrade",
		"install.phase.scan":          "Scan history",
		"install.phase.start_service": "Start service",
		"install.phase.health_check":  "Health check",
		"install.phase.complete":      "Installation complete.",
		"install.progress.scan":       "Scan history: %d files discovered, %d files processed, %d events added, %d notices.\n",

		"doctor.loopbackOK":      "%s:%d; not exposed publicly",
		"doctor.loopbackError":   "a non-loopback address is configured",
		"doctor.permissions":     "state directory access is restricted to the current user",
		"doctor.machineHost":     "database originally belonged to host %s; current host is %s. CODEX_USAGE_HOME may have been copied or synced, so the per-machine boundary is unreliable",
		"doctor.machinePlatform": "database platform is %s/%s; current platform is %s/%s. Do not sync CODEX_USAGE_HOME across machines",
		"doctor.database":        "%s; events=%d sessions=%d",
		"doctor.jsonlOnly":       "JSONL is the only token-accounting source; the state database is used only for discovery and metadata",
		"doctor.coverage":        "%d anomaly/coverage notices; review them in the Dashboard",
		"doctor.homeUnreadable":  "%s does not exist or is unreadable",
		"doctor.stateWarning":    "%s: %s; scanning %d JSONL files",
		"doctor.stateDB":         "%s: %d canonical rollouts",
		"doctor.stateMissing":    "%s has no recognizable state database; scanning %d JSONL files",
		"doctor.sharedHistory":   "session %s appears in both %s and %s. If multiple machines share one CODEX_HOME, pre-install history cannot be separated reliably; sessions are deduplicated",
		"doctor.legacyManaged":   "%s still contains a legacy codex-usage OTel managed stanza; install can remove only that block",
		"doctor.configUntouched": "%s needs no codex-usage exporter configuration; third-party settings are neither read nor changed",
		"doctor.sharedHome":      "%s is on a common cross-system mount; confirm it is not shared with Windows",
		"doctor.serviceOK":       "local Dashboard is reachable",
		"doctor.serviceDown":     "local service is not running",
		"doctor.identityOK":      "local Dashboard identity matches: %s %s (%s/%s)",
		"doctor.identityError":   "local Dashboard identity verification failed: %v",
		"doctor.failed":          "health checks found errors",
		"doctor.n.stateDir":      "State directory",
		"doctor.n.config":        "Config",
		"doctor.n.codexHome":     "Codex Home",
		"doctor.n.loopback":      "Loopback",
		"doctor.n.database":      "Database",
		"doctor.n.machine":       "Machine",
		"doctor.n.machineID":     "Machine identity",
		"doctor.n.accounting":    "Accounting",
		"doctor.n.coverage":      "Coverage",
		"doctor.n.stateDB":       "State database",
		"doctor.n.history":       "Shared history",
		"doctor.n.codexConfig":   "Codex config",
		"doctor.n.sharedHome":    "Shared Home",
		"doctor.n.serviceID":     "Service identity",
		"doctor.n.privacy":       "Privacy schema",
		"doctor.n.network":       "Network",
		"doctor.privacy":         "database schema contains counts, time, model, source, path, and title only; no prompt/reply/reasoning/auth fields",
		"doctor.network":         "runtime has no external reporting client; the Dashboard listens on loopback only",
	},
}

func Normalize(value string) (Locale, bool) {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch {
	case value == "en" || strings.HasPrefix(value, "en-"):
		return English, true
	case value == "zh" || strings.HasPrefix(value, "zh-"):
		return Chinese, true
	default:
		return "", false
	}
}

func Detect(explicit, environment, system string) (Locale, error) {
	if explicit != "" {
		locale, ok := Normalize(explicit)
		if !ok {
			return "", fmt.Errorf(messages[English]["error.invalidLang"], explicit)
		}
		return locale, nil
	}
	if locale, ok := Normalize(environment); ok {
		return locale, nil
	}
	if locale, ok := Normalize(system); ok {
		return locale, nil
	}
	return Chinese, nil
}

func (locale Locale) Text(key string, args ...any) string {
	if locale != English {
		locale = Chinese
	}
	format, ok := messages[locale][key]
	if !ok {
		format = messages[Chinese][key]
	}
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

func Catalogs() map[Locale]map[string]string { return messages }
