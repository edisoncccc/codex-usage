package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/meter"
	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/otel"
	"github.com/zJay26/codex-usage/internal/platform"
	meterServer "github.com/zJay26/codex-usage/internal/server"
	"github.com/zJay26/codex-usage/internal/store"
)

var (
	Version   = "0.1.0"
	Commit    = "dev"
	BuildDate = "unknown"
)

type CLI struct {
	Stdout io.Writer
	Stderr io.Writer
}

func (c CLI) Run(args []string) int {
	platform.SetPrivateUmask()
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			c.usage()
			return 0
		case "-v", "--version":
			fmt.Fprintf(c.Stdout, "codex-usage %s (%s, %s) %s/%s\n", Version, Commit, BuildDate, runtime.GOOS, runtime.GOARCH)
			return 0
		}
	}
	command := "open"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	var err error
	switch command {
	case "open":
		err = c.open(args)
	case "serve":
		err = c.serve(args, false)
	case "daemon":
		err = c.serve(args, true)
	case "install":
		err = c.install(args)
	case "uninstall":
		err = c.uninstall(args)
	case "scan":
		err = c.scan(args)
	case "summary":
		err = c.summary(args)
	case "doctor":
		err = c.doctor(args)
	case "config":
		err = c.config(args)
	case "version", "--version", "-v":
		fmt.Fprintf(c.Stdout, "codex-usage %s (%s, %s) %s/%s\n", Version, Commit, BuildDate, runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-h":
		c.usage()
		return 0
	default:
		fmt.Fprintf(c.Stderr, "未知命令 %q\n\n", command)
		c.usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(c.Stderr, "错误:", err)
		return 1
	}
	return 0
}

func (c CLI) usage() {
	fmt.Fprintln(c.Stdout, `Codex Usage — 当前电脑的 Codex Token 本地统计

用法:
  codex-usage                         打开本机 Dashboard
  codex-usage open                    打开本机 Dashboard
  codex-usage install                 用户级安装、初始化、配置 OTel 并启动
  codex-usage summary --since 7d      命令行摘要（支持 --json / --csv）
  codex-usage scan [--rebuild]        增量扫描本机 Codex session
  codex-usage doctor                  检查路径、配置、覆盖缺口与服务状态
  codex-usage config add-home PATH    添加一个额外 CODEX_HOME
  codex-usage uninstall [--purge]     卸载；默认保留统计库
  codex-usage serve                   前台运行本地服务
  codex-usage version                 显示版本

边界:
  “电脑”是运行 Codex 客户端和采集器的主机；不是 shell/tool 实际执行的远程环境。
  不读取 auth.json、prompt、回复、reasoning 或工具输出，也不估算费用/账号配额。`)
}

type runtimeState struct {
	paths    config.Paths
	cfg      config.Config
	homes    []string
	store    *store.Store
	scanner  *meter.Scanner
	receiver *otel.Receiver
	server   *meterServer.Server
}

type patchedHome struct {
	home   string
	result config.PatchResult
}

func openState() (*runtimeState, error) {
	paths, err := config.ResolvePaths()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return nil, err
	}
	homes, err := config.CodexHomes(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(paths.Database)
	if err != nil {
		return nil, err
	}
	if err := config.EnsureStateMarker(paths); err != nil {
		st.Close()
		return nil, err
	}
	if err := platform.LockDown(paths.StateDir); err != nil {
		st.Close()
		return nil, fmt.Errorf("收紧状态目录权限: %w", err)
	}
	scanner := &meter.Scanner{Store: st}
	receiver := otel.NewReceiver(st)
	srv := &meterServer.Server{
		Store: st, Scanner: scanner, Receiver: receiver,
		Homes: func() ([]string, error) {
			current, loadErr := config.Load(paths)
			if loadErr != nil {
				return nil, loadErr
			}
			return config.CodexHomes(current)
		},
		Address: cfg.ListenAddress, Port: cfg.Port, Version: Version,
	}
	return &runtimeState{
		paths: paths, cfg: cfg, homes: homes, store: st,
		scanner: scanner, receiver: receiver, server: srv,
	}, nil
}

func (c CLI) serve(args []string, daemon bool) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if daemon {
		platform.HideConsole()
	}
	state, err := openState()
	if err != nil {
		return err
	}
	defer state.store.Close()
	if daemon && healthOK(state.server.URL()) {
		return nil
	}
	if daemon {
		if err := config.EnsurePrivateDir(state.paths.StateDir); err != nil {
			return err
		}
		logPath := filepath.Join(state.paths.StateDir, "daemon.log")
		if file, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); openErr == nil {
			defer file.Close()
			log.SetOutput(file)
		}
	}
	removePID, err := platform.WritePID(state.paths.StateDir)
	if err != nil {
		return err
	}
	defer removePID()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.Signal(15))
	defer cancel()
	go func() {
		result, scanErr := state.scanner.Scan(ctx, state.homes, false)
		if scanErr != nil && !errors.Is(scanErr, context.Canceled) {
			log.Printf("initial scan: %v", scanErr)
		} else {
			log.Printf("initial scan: files=%d inserted=%d warnings=%d", result.Files, result.EventsInserted, result.Warnings)
		}
	}()
	go periodicScan(ctx, state, time.Duration(state.cfg.ScanIntervalSeconds)*time.Second)
	if !daemon {
		fmt.Fprintf(c.Stdout, "Codex Usage 正在 %s 运行；按 Ctrl+C 停止。\n", state.server.URL())
	}
	return state.server.Run(ctx)
}

func periodicScan(ctx context.Context, state *runtimeState, interval time.Duration) {
	if interval < 15*time.Second {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg, err := config.Load(state.paths)
			if err != nil {
				log.Printf("reload config: %v", err)
				continue
			}
			homes, err := config.CodexHomes(cfg)
			if err != nil {
				log.Printf("resolve homes: %v", err)
				continue
			}
			if _, err := state.scanner.Scan(ctx, homes, false); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("periodic scan: %v", err)
			}
		}
	}
}

func (c CLI) open(args []string) error {
	flags := flag.NewFlagSet("open", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return err
	}
	rawURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	if !healthOK(rawURL) {
		executable := paths.InstalledEXE
		if _, statErr := os.Stat(executable); statErr != nil {
			executable, err = os.Executable()
			if err != nil {
				return err
			}
		}
		if err := platform.StartDetached(executable, "daemon"); err != nil {
			return fmt.Errorf("启动本地服务: %w", err)
		}
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && !healthOK(rawURL) {
			time.Sleep(180 * time.Millisecond)
		}
		if !healthOK(rawURL) {
			return fmt.Errorf("本地服务未在 12 秒内就绪；请运行 codex-usage doctor")
		}
	}
	if platform.HasGUI() {
		if err := platform.OpenURL(rawURL); err == nil {
			fmt.Fprintln(c.Stdout, "已打开", rawURL)
			return nil
		}
	}
	hostname, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = "<user>"
	}
	if hostname == "" {
		hostname = "<server>"
	}
	fmt.Fprintln(c.Stdout, "Dashboard:", rawURL)
	fmt.Fprintf(c.Stdout, "无图形环境时，在你的电脑运行：ssh -N -L %d:127.0.0.1:%d %s@%s\n",
		cfg.Port, cfg.Port, user, hostname)
	return nil
}

func healthOK(baseURL string) bool {
	client := &http.Client{
		Timeout: 650 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
		},
	}
	response, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload); err != nil {
		return false
	}
	return payload.OK
}

func (c CLI) scan(args []string) error {
	flags := flag.NewFlagSet("scan", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	rebuild := flags.Bool("rebuild", false, "清空历史扫描结果后重建（保留 OTel）")
	asJSON := flags.Bool("json", false, "输出 JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	state, err := openState()
	if err != nil {
		return err
	}
	defer state.store.Close()
	result, err := state.scanner.Scan(context.Background(), state.homes, *rebuild)
	if err != nil {
		return err
	}
	if *asJSON {
		return writePrettyJSON(c.Stdout, result)
	}
	fmt.Fprintf(c.Stdout, "扫描完成：%d 个 Home，%d 个文件，新增 %d 个事件，忽略 %d 个重复，%d 条提示，耗时 %.2fs\n",
		result.Homes, result.Files, result.EventsInserted, result.Duplicates,
		result.Warnings, float64(result.ElapsedMillis)/1000)
	if result.Unattributed > 0 {
		fmt.Fprintf(c.Stdout, "有 %d 个 session 的差额仅计入“历史未归属”，未伪造每日分布。\n", result.Unattributed)
	}
	return nil
}

func (c CLI) summary(args []string) error {
	flags := flag.NewFlagSet("summary", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	since := flags.String("since", "7d", "范围：7d、30d、today、all 或 RFC3339")
	asJSON := flags.Bool("json", false, "输出 JSON")
	asCSV := flags.Bool("csv", false, "输出 CSV")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *asJSON && *asCSV {
		return errors.New("--json 与 --csv 不能同时使用")
	}
	state, err := openState()
	if err != nil {
		return err
	}
	defer state.store.Close()
	filter := model.Filter{}
	if *since != "all" {
		filter.Since, err = meterServer.ParseSince(*since)
		if err != nil {
			return err
		}
	}
	value, err := state.store.Summary(context.Background(), filter)
	if err != nil {
		return err
	}
	if *asJSON {
		return writePrettyJSON(c.Stdout, value)
	}
	if *asCSV {
		writer := csv.NewWriter(c.Stdout)
		_ = writer.Write([]string{"machine_id", "machine_label", "since", "input", "cached_input", "cache_write_input", "output", "reasoning_output", "attributed_total", "unattributed_total", "grand_total", "events", "sessions"})
		machine := state.store.Machine()
		_ = writer.Write([]string{
			machine.ID, safeCSVText(machine.Label), *since, strconv.FormatInt(value.Usage.Input, 10), strconv.FormatInt(value.Usage.CachedInput, 10),
			strconv.FormatInt(value.Usage.CacheWriteInput, 10), strconv.FormatInt(value.Usage.Output, 10),
			strconv.FormatInt(value.Usage.ReasoningOutput, 10), strconv.FormatInt(value.Usage.Total, 10),
			strconv.FormatInt(value.Unattributed.Total, 10), strconv.FormatInt(value.GrandTotal, 10),
			strconv.FormatInt(value.EventCount, 10), strconv.FormatInt(value.SessionCount, 10),
		})
		writer.Flush()
		return writer.Error()
	}
	fmt.Fprintf(c.Stdout, "Codex Usage · 当前电脑 · %s\n", *since)
	fmt.Fprintf(c.Stdout, "Total             %s\n", comma(value.GrandTotal))
	fmt.Fprintf(c.Stdout, "Input             %s\n", comma(value.Usage.Input))
	fmt.Fprintf(c.Stdout, "  Cached Input    %s  (Input 的子集)\n", comma(value.Usage.CachedInput))
	fmt.Fprintf(c.Stdout, "  Cache Write     %s\n", comma(value.Usage.CacheWriteInput))
	fmt.Fprintf(c.Stdout, "Output            %s\n", comma(value.Usage.Output))
	fmt.Fprintf(c.Stdout, "  Reasoning       %s  (Output 的子集)\n", comma(value.Usage.ReasoningOutput))
	fmt.Fprintf(c.Stdout, "Events / Sessions %d / %d\n", value.EventCount, value.SessionCount)
	if value.Unattributed.Total > 0 {
		fmt.Fprintf(c.Stdout, "历史未归属        %s  (只属于累计，不属于所选日期)\n", comma(value.Unattributed.Total))
	}
	return nil
}

func (c CLI) config(args []string) error {
	if len(args) < 1 {
		return errors.New("用法: codex-usage config add-home PATH")
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add-home":
		if len(args) != 2 {
			return errors.New("用法: codex-usage config add-home PATH")
		}
		added, err := config.AddHome(paths, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(c.Stdout, "已添加额外 Codex Home:", added)
		fmt.Fprintln(c.Stdout, "运行 codex-usage scan 开始统计。")
		return nil
	default:
		return fmt.Errorf("未知 config 子命令 %q", args[0])
	}
}

func (c CLI) install(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	skipScan := flags.Bool("skip-scan", false, "跳过首次历史扫描")
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return err
	}
	if explicitHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); explicitHome != "" {
		if absoluteHome, absErr := filepath.Abs(explicitHome); absErr == nil {
			cfg.ExtraCodexHomes = append(cfg.ExtraCodexHomes, absoluteHome)
		}
	}
	if err := config.EnsurePrivateDir(paths.StateDir); err != nil {
		return err
	}
	if err := platform.LockDown(paths.StateDir); err != nil {
		fmt.Fprintln(c.Stderr, "警告：无法进一步收紧状态目录权限:", err)
	}
	if err := config.EnsurePrivateDir(paths.InstallDir); err != nil {
		return err
	}
	if err := config.Save(paths, cfg); err != nil {
		return err
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	source, _ = filepath.Abs(source)
	destination, _ := filepath.Abs(paths.InstalledEXE)
	if _, statErr := os.Stat(destination); statErr == nil {
		_ = platform.UninstallService(paths.StateDir)
	}
	if !sameFilePath(source, destination) {
		if err := copyExecutable(source, destination); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.Stdout, "已安装:", destination)

	state, err := openState()
	if err != nil {
		return err
	}
	if !*skipScan {
		fmt.Fprintln(c.Stdout, "正在执行首次本地历史扫描（只解析 metadata 与 token_count）…")
		result, scanErr := state.scanner.Scan(context.Background(), state.homes, false)
		if scanErr != nil {
			fmt.Fprintln(c.Stderr, "警告：首次扫描未完成:", scanErr)
		} else {
			fmt.Fprintf(c.Stdout, "首次扫描：%d 文件，新增 %d 事件，%d 条提示。\n",
				result.Files, result.EventsInserted, result.Warnings)
		}
	}
	state.store.Close()

	homes, err := config.CodexHomes(cfg)
	if err != nil {
		return err
	}
	var patched []patchedHome
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/v1/metrics", cfg.Port)
	for _, home := range homes {
		result, patchErr := config.InstallOTel(home, endpoint, paths.BackupDir)
		if patchErr != nil {
			rollbackPatches(patched)
			return fmt.Errorf("%s: %w", home, patchErr)
		}
		fmt.Fprintf(c.Stdout, "Codex Home %s：%s\n", home, result.Message)
		if result.Conflict {
			fmt.Fprintln(c.Stderr, "警告：已有 metrics exporter 未被覆盖；此 Home 仅使用历史扫描。")
		}
		if result.Changed {
			patched = append(patched, patchedHome{home: home, result: result})
			if validateErr := validateCodexConfig(home); validateErr != nil {
				rollbackPatches(patched)
				return fmt.Errorf("Codex 严格配置检查失败，已回滚 %s: %w", home, validateErr)
			}
		}
	}
	serviceResult, err := platform.InstallService(destination, paths.StateDir)
	if err != nil {
		rollbackPatches(patched)
		return err
	}
	serviceURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) && !healthOK(serviceURL) {
		time.Sleep(180 * time.Millisecond)
	}
	if !healthOK(serviceURL) {
		_ = platform.UninstallService(paths.StateDir)
		rollbackPatches(patched)
		return fmt.Errorf("后台服务未在 12 秒内通过本机 health check；OTel 配置已回滚")
	}
	if serviceResult.Detail != "" {
		fmt.Fprintln(c.Stdout, "后台服务:", serviceResult.Detail)
	}
	if serviceResult.Warning != "" {
		fmt.Fprintln(c.Stderr, "警告:", serviceResult.Warning)
	}
	fmt.Fprintf(c.Stdout, "Dashboard: http://127.0.0.1:%d\n", cfg.Port)
	fmt.Fprintln(c.Stdout, "安装完成。请在方便时自行重启 Codex，使 OTel 配置对新进程生效；不会强制结束当前任务。")
	return nil
}

func rollbackPatches(items []patchedHome) {
	for i := len(items) - 1; i >= 0; i-- {
		_ = config.RollbackOTel(items[i].home, items[i].result.Backup)
	}
}

func (c CLI) uninstall(args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	purge := flags.Bool("purge", false, "同时删除统计库和工具配置")
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, loadErr := config.Load(paths)
	if loadErr != nil {
		cfg = config.Default()
	}
	homes, _ := config.CodexHomes(cfg)
	if err := platform.UninstallService(paths.StateDir); err != nil {
		return err
	}
	for _, home := range homes {
		changed, removeErr := config.UninstallOTel(home)
		if removeErr != nil {
			return fmt.Errorf("移除 %s managed stanza: %w", home, removeErr)
		}
		if changed {
			fmt.Fprintln(c.Stdout, "已移除工具管理的 OTel 配置:", home)
		}
	}
	if err := platform.RemoveInstalledExecutable(paths.InstalledEXE, paths.StateDir, *purge); err != nil {
		return err
	}
	if *purge {
		fmt.Fprintln(c.Stdout, "已卸载服务、工具和统计数据（不可从工具内恢复）。")
	} else {
		fmt.Fprintln(c.Stdout, "已卸载服务和工具；统计库保留在:", paths.Database)
		fmt.Fprintln(c.Stdout, "如需删除数据，请显式运行 codex-usage uninstall --purge。")
	}
	return nil
}

func (c CLI) doctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	asJSON := flags.Bool("json", false, "输出 JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths)
	if err != nil {
		return err
	}
	homes, err := config.CodexHomes(cfg)
	if err != nil {
		return err
	}
	type check struct {
		Level  string `json:"level"`
		Name   string `json:"name"`
		Detail string `json:"detail"`
	}
	var checks []check
	add := func(level, name, detail string) { checks = append(checks, check{level, name, detail}) }
	if cfg.ListenAddress == "127.0.0.1" || cfg.ListenAddress == "localhost" {
		add("ok", "loopback", fmt.Sprintf("%s:%d；不会监听公网", cfg.ListenAddress, cfg.Port))
	} else {
		add("error", "loopback", "配置了非 loopback 地址")
	}
	st, dbErr := store.Open(paths.Database)
	if dbErr != nil {
		add("error", "database", dbErr.Error())
	} else {
		if markerErr := config.EnsureStateMarker(paths); markerErr != nil {
			add("warn", "state_marker", markerErr.Error())
		}
		if permissionErr := platform.LockDown(paths.StateDir); permissionErr != nil {
			add("warn", "permissions", permissionErr.Error())
		} else {
			add("ok", "permissions", "状态目录已限制为当前用户访问")
		}
		status, statusErr := st.Status(context.Background())
		if statusErr != nil {
			add("error", "database", statusErr.Error())
		} else {
			add("ok", "machine", fmt.Sprintf("%s / %s (%s/%s)", status.Machine.Label, status.Machine.ID, status.Machine.OS, status.Machine.Arch))
			if currentHostname, hostErr := os.Hostname(); hostErr == nil &&
				currentHostname != "" && !strings.EqualFold(currentHostname, status.Machine.Hostname) {
				add("warn", "machine_identity",
					fmt.Sprintf("数据库最初属于主机 %s，当前主机为 %s；可能复制/同步了 CODEX_USAGE_HOME，逐电脑边界不再可靠", status.Machine.Hostname, currentHostname))
			}
			if status.Machine.OS != runtime.GOOS || status.Machine.Arch != runtime.GOARCH {
				add("warn", "machine_identity",
					fmt.Sprintf("数据库记录平台为 %s/%s，当前为 %s/%s；请勿跨电脑同步 CODEX_USAGE_HOME",
						status.Machine.OS, status.Machine.Arch, runtime.GOOS, runtime.GOARCH))
			}
			add("ok", "database", fmt.Sprintf("%s；events=%d sessions=%d", paths.Database, status.EventCount, status.SessionCount))
			if status.OTelActive {
				add("ok", "otel", "最近两分钟收到 turn.token_usage")
			} else {
				add("warn", "otel", "当前未实时接收；历史扫描仍可工作")
			}
			if status.WarningCount > 0 {
				add("warn", "coverage", fmt.Sprintf("%d 条异常/覆盖提示，请在 Dashboard 查看", status.WarningCount))
			}
			if len(status.CoverageGaps) > 0 {
				add("warn", "coverage", fmt.Sprintf("记录到 %d 段 OTel 服务离线缺口；缺口区间由 session 扫描补位", len(status.CoverageGaps)))
			}
		}
		st.Close()
	}

	sessionHomes := map[string]string{}
	for _, home := range homes {
		info, statErr := os.Stat(home)
		if statErr != nil || !info.IsDir() {
			add("warn", "codex_home", fmt.Sprintf("%s 不存在或不可读", home))
			continue
		}
		discovery := meter.DiscoverHome(context.Background(), home)
		if discovery.Warning != "" {
			add("warn", "state_db", fmt.Sprintf("%s：%s；扫描 %d 个 JSONL", home, discovery.Warning, len(discovery.Paths)))
		} else if discovery.StateDB != "" && !discovery.Fallback {
			add("ok", "state_db", fmt.Sprintf("%s：%d 个 canonical rollout", discovery.StateDB, len(discovery.Paths)))
		} else {
			add("warn", "state_db", fmt.Sprintf("%s 没有可识别状态库；扫描 %d 个 JSONL", home, len(discovery.Paths)))
		}
		for _, session := range discovery.Sessions {
			if previous, ok := sessionHomes[session.SessionID]; ok && !sameFilePath(previous, home) {
				add("warn", "shared_history",
					fmt.Sprintf("session %s 同时出现在 %s 与 %s。若多台电脑同步同一 CODEX_HOME，安装前历史无法可靠拆分；统计会按 session 去重。", session.SessionID, previous, home))
				break
			}
			sessionHomes[session.SessionID] = home
		}
		configData, _ := os.ReadFile(config.CodexConfigPath(home))
		hasManaged := bytes.Contains(configData, []byte("# BEGIN codex-usage managed"))
		hasExporter := bytes.Contains(configData, []byte("metrics_exporter"))
		switch {
		case hasManaged:
			add("ok", "codex_config", home+" 已包含 codex-usage managed exporter")
		case hasExporter:
			add("warn", "codex_config", home+" 已有第三方 metrics_exporter；工具不会覆盖，实时采集冲突")
		default:
			add("warn", "codex_config", home+" 尚未配置实时 OTel；运行 install 可安全添加")
		}
		if runtime.GOOS == "linux" && strings.Contains(filepath.ToSlash(home), "/mnt/") {
			add("warn", "shared_home", home+" 位于常见跨系统挂载路径；确认它不是与 Windows 共用的 CODEX_HOME")
		}
	}
	if healthOK(fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)) {
		add("ok", "service", "本地 Dashboard 可访问")
	} else {
		add("warn", "service", "本地服务未运行")
	}
	add("ok", "privacy_schema", "数据库 schema 仅含计数、时间、模型、来源、路径和标题；无 prompt/reply/reasoning/auth 字段")
	add("ok", "network", "运行时无外部上报客户端；Dashboard 与 OTLP 仅使用 loopback")

	if *asJSON {
		return writePrettyJSON(c.Stdout, map[string]any{"checks": checks, "paths": paths, "homes": homes})
	}
	for _, item := range checks {
		symbol := map[string]string{"ok": "✓", "warn": "!", "error": "✗"}[item.Level]
		fmt.Fprintf(c.Stdout, "%s %-16s %s\n", symbol, item.Name, item.Detail)
	}
	return nil
}

func validateCodexConfig(home string) error {
	binary, err := exec.LookPath("codex")
	if err != nil {
		// The TOML parser already performed semantic validation. Desktop-only
		// installs may legitimately have no CLI on PATH.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "features", "list")
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".codex-usage-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := io.Copy(temp, input); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0o755); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		deadline := time.Now().Add(3 * time.Second)
		for {
			removeErr := os.Remove(destination)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				break
			}
			if time.Now().After(deadline) {
				return removeErr
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return os.Rename(tempName, destination)
}

func sameFilePath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func writePrettyJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func comma(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	raw := strconv.FormatInt(value, 10)
	for i := len(raw) - 3; i > 0; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	if negative {
		raw = "-" + raw
	}
	return raw
}

func safeCSVText(value string) string {
	if value == "" {
		return ""
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}
