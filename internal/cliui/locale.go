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
		"error.prefix":         "错误:",
		"error.invalidLang":    "不支持语言 %q；可选值为 en 或 zh-CN",
		"error.langMissing":    "--lang 需要一个值；可选值为 en 或 zh-CN",
		"error.unknownCommand": "未知命令 %q",
		"usage": `Codex Usage — 当前电脑的 Codex Token 本地统计

用法:
  codex-usage [--lang en|zh-CN]       打开本机 Dashboard
  codex-usage open                    打开本机 Dashboard
  codex-usage install                 用户级安装、初始化 JSONL 扫描并启动
  codex-usage summary --since 7d      命令行摘要（支持 --json / --csv）
  codex-usage scan [--rebuild]        增量扫描本机 Codex session
  codex-usage doctor                  检查路径、JSONL 数据源与服务状态
  codex-usage config add-home PATH    添加一个额外 CODEX_HOME
  codex-usage uninstall [--purge]     卸载；默认保留统计库
  codex-usage serve                   前台运行本地服务
  codex-usage version                 显示版本

语言:
  --lang 优先于 CODEX_USAGE_LANG，其次跟随系统语言；支持 en、zh-CN。

边界:
  “电脑”是运行 Codex 客户端和 codex-usage 的主机；不是 shell/tool 实际执行的远程环境。
  不读取 auth.json、prompt、回复、reasoning 或工具输出；不读取真实账单或账号配额，
  仅按本机 Token 与内置 Standard API 价格提供等价成本估算。`,
		"serve.running":          "Codex Usage 正在 %s 运行；按 Ctrl+C 停止。\n",
		"open.start":             "启动本地服务: %w",
		"open.notReady":          "本地服务未在 12 秒内就绪；请运行 codex-usage doctor",
		"open.opened":            "已打开",
		"open.noGUI":             "无图形环境时，在你的电脑运行：ssh -N -L %d:127.0.0.1:%d %s@%s\n",
		"flag.rebuild":           "清空派生统计后从全部 JSONL 重建",
		"flag.json":              "输出 JSON",
		"flag.csv":               "输出 CSV",
		"flag.since":             "范围：7d、30d、today、all 或 RFC3339",
		"flag.skipScan":          "跳过首次历史扫描",
		"flag.purge":             "同时删除统计库和工具配置",
		"summary.conflict":       "--json 与 --csv 不能同时使用",
		"summary.title":          "Codex Usage · 当前电脑 · %s\n",
		"summary.cached":         "  Cached Input    %s  (Input 的子集)\n",
		"summary.reasoning":      "  Reasoning       %s  (Output 的子集)\n",
		"summary.unattributed":   "历史未归属        %s  (只属于累计，不属于所选日期)\n",
		"scan.complete":          "扫描完成：%d 个 Home，%d 个文件，新增 %d 个事件，忽略 %d 个重复，%d 条提示，耗时 %.2fs\n",
		"scan.unattributed":      "有 %d 个 session 的差额仅计入“历史未归属”，未伪造每日分布。\n",
		"install.stopOld":        "停止旧版后台服务: %w",
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
		"install.health":         "后台服务未在 12 秒内通过本机 health check",
		"install.service":        "后台服务:",
		"install.warning":        "警告:",
		"install.cleanup":        "警告：旧版可执行文件未能自动清理:",
		"install.done":           "安装完成。后台服务会定期增量扫描本机 JSONL；无需重启 Codex。",
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
		"doctor.privacy":         "数据库 schema 仅含计数、时间、模型、来源、路径和标题；无 prompt/reply/reasoning/auth 字段",
		"doctor.network":         "运行时无外部上报客户端；Dashboard 仅监听 loopback",
	},
	English: {
		"error.prefix":         "Error:",
		"error.invalidLang":    "unsupported language %q; use en or zh-CN",
		"error.langMissing":    "--lang requires a value; use en or zh-CN",
		"error.unknownCommand": "unknown command %q",
		"usage": `Codex Usage — local Codex token accounting for this machine

Usage:
  codex-usage [--lang en|zh-CN]       Open the local Dashboard
  codex-usage open                    Open the local Dashboard
  codex-usage install                 Install for this user, initialize JSONL scanning, and start
  codex-usage summary --since 7d      Print a CLI summary (supports --json / --csv)
  codex-usage scan [--rebuild]        Incrementally scan local Codex sessions
  codex-usage doctor                  Check paths, JSONL sources, and service state
  codex-usage config add-home PATH    Add another CODEX_HOME
  codex-usage uninstall [--purge]     Uninstall; keep the usage database by default
  codex-usage serve                   Run the local service in the foreground
  codex-usage version                 Print the version

Language:
  --lang overrides CODEX_USAGE_LANG, then the system locale; supports en and zh-CN.

Boundary:
  “Machine” means the host running the Codex client and codex-usage, not a remote shell/tool target.
  Codex Usage never reads auth.json, prompts, replies, reasoning, or tool output. It does not read
  bills or account quotas; cost is only a Standard API-equivalent estimate of local token usage.`,
		"serve.running":          "Codex Usage is running at %s; press Ctrl+C to stop.\n",
		"open.start":             "start local service: %w",
		"open.notReady":          "the local service was not ready within 12 seconds; run codex-usage doctor",
		"open.opened":            "Opened",
		"open.noGUI":             "With no graphical session, run this on your computer: ssh -N -L %d:127.0.0.1:%d %s@%s\n",
		"flag.rebuild":           "Clear derived accounting and rebuild it from all JSONL files",
		"flag.json":              "Output JSON",
		"flag.csv":               "Output CSV",
		"flag.since":             "Range: 7d, 30d, today, all, or RFC3339",
		"flag.skipScan":          "Skip the first historical scan",
		"flag.purge":             "Also delete the usage database and tool configuration",
		"summary.conflict":       "--json and --csv cannot be used together",
		"summary.title":          "Codex Usage · this machine · %s\n",
		"summary.cached":         "  Cached Input    %s  (subset of Input)\n",
		"summary.reasoning":      "  Reasoning       %s  (subset of Output)\n",
		"summary.unattributed":   "Historical only   %s  (part of all-time totals, not the selected dates)\n",
		"scan.complete":          "Scan complete: %d Homes, %d files, %d events added, %d duplicates ignored, %d notices, %.2fs\n",
		"scan.unattributed":      "%d sessions have deltas counted only as historical unattributed usage; no daily distribution was invented.\n",
		"install.stopOld":        "stop previous background service: %w",
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
		"install.health":         "the background service failed its local health check within 12 seconds",
		"install.service":        "Background service:",
		"install.warning":        "Warning:",
		"install.cleanup":        "Warning: the previous executable could not be removed automatically:",
		"install.done":           "Installation complete. The background service incrementally scans local JSONL files; Codex does not need to restart.",
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
