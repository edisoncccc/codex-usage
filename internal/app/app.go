package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zJay26/codex-usage/internal/cliui"
	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/model"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/pricing"
	usageServer "github.com/zJay26/codex-usage/internal/server"
	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
)

const activityProbeInterval = 30 * time.Second

type CLI struct {
	Stdout            io.Writer
	Stderr            io.Writer
	Stdin             io.Reader
	Now               func() time.Time
	HeartbeatInterval time.Duration
	locale            cliui.Locale
	openScanState     func() (*scanRuntime, error)
	doctorDeps        *doctorDependencies
}

func (c CLI) Run(args []string) int {
	platform.SetPrivateUmask()
	c = c.withDefaults()
	explicitLanguage, remaining, languageErr := extractLanguage(args)
	var locale cliui.Locale
	if languageErr == nil {
		locale, languageErr = cliui.Detect(explicitLanguage, os.Getenv("CODEX_USAGE_LANG"), platform.UserLocale())
	}
	if languageErr != nil {
		if phase, structured := structuredCommandPhase(args); structured {
			return c.runStructured(phase, args, func([]string, *eventEmitter) (commandResult, error) {
				return commandResult{}, &codedError{
					Code:     "invalid_arguments",
					ExitCode: 2,
					Err:      languageErr,
				}
			})
		}
		fmt.Fprintln(c.Stderr, cliui.English.Text("error.prefix"), languageErr)
		return 2
	}
	c.locale = locale
	args = remaining
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			c.usage()
			return 0
		case "-v", "--version":
			c.printHumanVersion()
			return 0
		}
	}
	command, args := splitCommand(args)
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
		return c.runStructured("scan", args, c.scanCommand)
	case "summary":
		err = c.summary(args)
	case "doctor":
		return c.runStructured("doctor", args, c.doctorCommand)
	case "config":
		err = c.config(args)
	case "version", "--version", "-v":
		return c.runStructured("version", args, c.versionCommand)
	case "help", "--help", "-h":
		c.usage()
		return 0
	default:
		fmt.Fprintln(c.Stderr, c.tr("error.unknownCommand", command))
		fmt.Fprintln(c.Stderr)
		c.usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(c.Stderr, c.tr("error.prefix"), err)
		return 1
	}
	return 0
}

func (c CLI) withDefaults() CLI {
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	if c.Stdin == nil {
		c.Stdin = os.Stdin
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 4 * time.Second
	}
	if c.openScanState == nil {
		c.openScanState = defaultOpenScanState
	}
	return c
}

func (c CLI) usage() {
	fmt.Fprintln(c.Stdout, c.tr("usage"))
}

func (c CLI) tr(key string, args ...any) string {
	return c.locale.Text(key, args...)
}

func extractLanguage(args []string) (string, []string, error) {
	remaining := make([]string, 0, len(args))
	explicit := ""
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--lang":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				partial := append([]string(nil), remaining...)
				partial = append(partial, args[index+1:]...)
				return "", partial, errors.New(cliui.English.Text("error.langMissing"))
			}
			explicit = args[index+1]
			index++
		case strings.HasPrefix(argument, "--lang="):
			explicit = strings.TrimPrefix(argument, "--lang=")
			if explicit == "" {
				partial := append([]string(nil), remaining...)
				partial = append(partial, args[index+1:]...)
				return "", partial, errors.New(cliui.English.Text("error.langMissing"))
			}
		default:
			remaining = append(remaining, argument)
		}
	}
	return explicit, remaining, nil
}

func structuredCommandPhase(args []string) (string, bool) {
	_, remaining, _ := extractLanguage(args)
	command, commandArgs := splitCommand(remaining)
	enabled, _ := extractJSONArgument(commandArgs)
	if !enabled {
		return "", false
	}
	switch command {
	case "version", "install", "scan", "doctor", "uninstall":
		return command, true
	default:
		return "", false
	}
}

func splitCommand(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "open", args
}

type runtimeState struct {
	paths   config.Paths
	cfg     config.Config
	homes   []string
	store   *store.Store
	scanner *usage.Scanner
	server  *usageServer.Server
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
	identity, err := currentBuildIdentity()
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
	scanner := &usage.Scanner{Store: st}
	srv := &usageServer.Server{
		Store: st, Scanner: scanner,
		Homes: func() ([]string, error) {
			current, loadErr := config.Load(paths)
			if loadErr != nil {
				return nil, loadErr
			}
			return config.CodexHomes(current)
		},
		LoadPricingOverrides: func() (map[string]pricing.Override, error) {
			current, loadErr := config.Load(paths)
			if loadErr != nil {
				return nil, loadErr
			}
			return current.PricingOverrides, nil
		},
		SavePricingOverrides: func(overrides map[string]pricing.Override) error {
			current, loadErr := config.Load(paths)
			if loadErr != nil {
				return loadErr
			}
			current.PricingOverrides = overrides
			return config.Save(paths, current)
		},
		Address: cfg.ListenAddress, Port: cfg.Port,
		BuildIdentity: usageServer.BuildIdentity{
			Version: identity.Version, Commit: identity.Commit, Dirty: identity.Dirty,
			BuildDate: identity.BuildDate, OS: identity.OS, Arch: identity.Arch,
		},
	}
	return &runtimeState{
		paths: paths, cfg: cfg, homes: homes, store: st,
		scanner: scanner, server: srv,
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
	go backgroundScan(ctx, state, time.Duration(state.cfg.ScanIntervalSeconds)*time.Second)
	if !daemon {
		fmt.Fprintf(c.Stdout, c.tr("serve.running"), state.server.URL())
	}
	return state.server.Run(ctx)
}

func backgroundScan(ctx context.Context, state *runtimeState, fallbackInterval time.Duration) {
	if fallbackInterval < 10*time.Minute {
		fallbackInterval = 10 * time.Minute
	}
	probe := &usage.ActivityProbe{}
	_, _ = probe.Changed(ctx, state.homes)
	activityTicker := time.NewTicker(activityProbeInterval)
	fallbackTicker := time.NewTicker(fallbackInterval)
	defer activityTicker.Stop()
	defer fallbackTicker.Stop()

	loadHomes := func() ([]string, error) {
		cfg, err := config.Load(state.paths)
		if err != nil {
			return nil, fmt.Errorf("reload config: %w", err)
		}
		homes, err := config.CodexHomes(cfg)
		if err != nil {
			return nil, fmt.Errorf("resolve homes: %w", err)
		}
		return homes, nil
	}
	runScan := func(reason string, homes []string) {
		result, err := state.scanner.Scan(ctx, homes, false)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("%s scan: %v", reason, err)
			return
		}
		if result.EventsInserted > 0 || result.Warnings > 0 {
			log.Printf("%s scan: files=%d inserted=%d warnings=%d elapsed_ms=%d",
				reason, result.Files, result.EventsInserted, result.Warnings, result.ElapsedMillis)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-activityTicker.C:
			homes, err := loadHomes()
			if err != nil {
				log.Printf("activity probe: %v", err)
				continue
			}
			changed, err := probe.Changed(ctx, homes)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("activity probe: %v", err)
				}
				continue
			}
			if changed {
				runScan("activity", homes)
			}
		case <-fallbackTicker.C:
			homes, err := loadHomes()
			if err != nil {
				log.Printf("fallback scan: %v", err)
				continue
			}
			runScan("fallback", homes)
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
			return fmt.Errorf(c.tr("open.start"), err)
		}
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && !healthOK(rawURL) {
			time.Sleep(180 * time.Millisecond)
		}
		if !healthOK(rawURL) {
			return errors.New(c.tr("open.notReady"))
		}
	}
	if platform.HasGUI() {
		if err := platform.OpenURL(rawURL); err == nil {
			fmt.Fprintln(c.Stdout, c.tr("open.opened"), rawURL)
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
	fmt.Fprintf(c.Stdout, c.tr("open.noGUI"),
		cfg.Port, cfg.Port, user, hostname)
	return nil
}

func healthOK(baseURL string) bool {
	identity, err := currentBuildIdentity()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 650*time.Millisecond)
	defer cancel()
	return probeIdentity(ctx, baseURL, identity) == nil
}

func (c CLI) summary(args []string) error {
	flags := flag.NewFlagSet("summary", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	since := flags.String("since", "7d", c.tr("flag.since"))
	asJSON := flags.Bool("json", false, c.tr("flag.json"))
	asCSV := flags.Bool("csv", false, c.tr("flag.csv"))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *asJSON && *asCSV {
		return errors.New(c.tr("summary.conflict"))
	}
	state, err := openState()
	if err != nil {
		return err
	}
	defer state.store.Close()
	filter := model.Filter{}
	if *since != "all" {
		filter.Since, err = usageServer.ParseSince(*since)
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
	fmt.Fprintf(c.Stdout, c.tr("summary.title"), *since)
	fmt.Fprintf(c.Stdout, "Total             %s\n", comma(value.GrandTotal))
	fmt.Fprintf(c.Stdout, "Input             %s\n", comma(value.Usage.Input))
	fmt.Fprintf(c.Stdout, c.tr("summary.cached"), comma(value.Usage.CachedInput))
	fmt.Fprintf(c.Stdout, "  Cache Write     %s\n", comma(value.Usage.CacheWriteInput))
	fmt.Fprintf(c.Stdout, "Output            %s\n", comma(value.Usage.Output))
	fmt.Fprintf(c.Stdout, c.tr("summary.reasoning"), comma(value.Usage.ReasoningOutput))
	fmt.Fprintf(c.Stdout, "Events / Sessions %d / %d\n", value.EventCount, value.SessionCount)
	if value.Unattributed.Total > 0 {
		fmt.Fprintf(c.Stdout, c.tr("summary.unattributed"), comma(value.Unattributed.Total))
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
	skipScan := flags.Bool("skip-scan", false, c.tr("flag.skipScan"))
	if err := flags.Parse(args); err != nil {
		return err
	}
	paths, err := config.ResolvePaths()
	if err != nil {
		return err
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	source, _ = filepath.Abs(source)
	destination, _ := filepath.Abs(paths.InstalledEXE)
	if _, statErr := os.Stat(destination); statErr == nil {
		if err := platform.UninstallService(destination, paths.StateDir); err != nil {
			return fmt.Errorf(c.tr("install.stopCurrent"), err)
		}
	}
	previous, err := config.ResolvePreviousPaths()
	if err != nil {
		return err
	}
	previousService := platform.PreviousService{
		StateDir: previous.StateDir, Executable: previous.InstalledEXE, InstallDir: previous.InstallDir,
		PIDPath: previous.PIDPath, LauncherPath: previous.LauncherPath,
		StartupEntry: previous.StartupEntry, ServiceName: previous.ServiceName,
	}
	_, previousStateErr := os.Stat(previous.StateDir)
	_, previousExecutableErr := os.Stat(previous.InstalledEXE)
	previousInstalled := previousStateErr == nil || previousExecutableErr == nil
	if err := platform.StopPreviousService(previousService); err != nil {
		return fmt.Errorf(c.tr("install.stopOld"), err)
	}
	migration, err := config.MigratePreviousState(paths, previous)
	if err != nil {
		return fmt.Errorf(c.tr("install.migrateOld"), err)
	}
	if migration.DatabaseConflict {
		return errors.New(c.tr("install.dbConflict"))
	}
	if migration.Found {
		fmt.Fprintln(c.Stdout, c.tr("install.migrated"))
		if previousInstalled && !migration.PreviousStateGone {
			fmt.Fprintln(c.Stderr, c.tr("install.oldState"), previous.StateDir)
		}
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
		fmt.Fprintln(c.Stderr, c.tr("install.permissions"), err)
	}
	if err := config.EnsurePrivateDir(paths.InstallDir); err != nil {
		return err
	}
	if err := config.Save(paths, cfg); err != nil {
		return err
	}
	if !sameFilePath(source, destination) {
		if err := copyExecutable(source, destination); err != nil {
			return err
		}
	}
	fmt.Fprintln(c.Stdout, c.tr("install.installed"), destination)

	state, err := openState()
	if err != nil {
		return err
	}
	if !*skipScan {
		fmt.Fprintln(c.Stdout, c.tr("install.scanning"))
		result, scanErr := state.scanner.Scan(context.Background(), state.homes, false)
		if scanErr != nil {
			fmt.Fprintln(c.Stderr, c.tr("install.scanWarning"), scanErr)
		} else {
			fmt.Fprintf(c.Stdout, c.tr("install.scanDone"),
				result.Files, result.EventsInserted, result.Warnings)
		}
	}
	state.store.Close()

	homes, err := config.CodexHomes(cfg)
	if err != nil {
		return err
	}
	for _, home := range homes {
		changed, removeErr := config.RemoveLegacyManagedOTel(home)
		if removeErr != nil {
			return fmt.Errorf("%s: %w", home, removeErr)
		}
		if changed {
			fmt.Fprintln(c.Stdout, c.tr("install.legacyRemoved", home))
		}
	}
	serviceResult, err := platform.InstallService(destination, paths.StateDir)
	if err != nil {
		return err
	}
	serviceURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !healthOK(serviceURL) {
		time.Sleep(180 * time.Millisecond)
	}
	if !healthOK(serviceURL) {
		_ = platform.UninstallService(destination, paths.StateDir)
		return errors.New(c.tr("install.health"))
	}
	if serviceResult.Detail != "" {
		fmt.Fprintln(c.Stdout, c.tr("install.service"), serviceResult.Detail)
	}
	if serviceResult.Warning != "" {
		fmt.Fprintln(c.Stderr, c.tr("install.warning"), serviceResult.Warning)
	}
	if previousInstalled {
		if cleanupErr := platform.RemovePreviousExecutable(previousService); cleanupErr != nil {
			fmt.Fprintln(c.Stderr, c.tr("install.cleanup"), cleanupErr)
		}
	}
	fmt.Fprintf(c.Stdout, "Dashboard: http://127.0.0.1:%d\n", cfg.Port)
	fmt.Fprintln(c.Stdout, c.tr("install.done"))
	return nil
}

func (c CLI) uninstall(args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(c.Stderr)
	purge := flags.Bool("purge", false, c.tr("flag.purge"))
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
	if err := platform.UninstallService(paths.InstalledEXE, paths.StateDir); err != nil {
		return err
	}
	for _, home := range homes {
		changed, removeErr := config.RemoveLegacyManagedOTel(home)
		if removeErr != nil {
			return fmt.Errorf("移除 %s managed stanza: %w", home, removeErr)
		}
		if changed {
			fmt.Fprintln(c.Stdout, "已移除旧版工具管理的 OTel 配置:", home)
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
