package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zJay26/codex-usage/internal/config"
	"github.com/zJay26/codex-usage/internal/platform"
	"github.com/zJay26/codex-usage/internal/store"
	"github.com/zJay26/codex-usage/internal/usage"
)

const (
	healthResponseLimit  int64 = 16 << 10
	healthRequestTimeout       = 5 * time.Second
)

type doctorCheck struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

type doctorPaths struct {
	StateDir     string `json:"state_dir"`
	ConfigPath   string `json:"config_path"`
	DatabasePath string `json:"database_path"`
	InstallDir   string `json:"install_dir"`
	Executable   string `json:"executable"`
}

type doctorReport struct {
	Status string        `json:"status"`
	Checks []doctorCheck `json:"checks"`
	Paths  doctorPaths   `json:"paths"`
	Homes  []string      `json:"homes"`
}

type doctorDependencies struct {
	ResolvePaths      func() (config.Paths, error)
	LoadConfig        func(config.Paths) (config.Config, error)
	ResolveHomes      func(config.Config) ([]string, error)
	EnsureStateMarker func(config.Paths) error
	LockDown          func(string) error
	HTTPClient        *http.Client
	HealthEndpoint    string
}

type identityProbeError struct {
	Code  string
	Field string
	Err   error
}

func (e *identityProbeError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Code, e.Field, e.Err)
}

func (e *identityProbeError) Unwrap() error { return e.Err }

type healthIdentityPayload struct {
	OK        *bool   `json:"ok"`
	Version   *string `json:"version"`
	Commit    *string `json:"commit"`
	Dirty     *bool   `json:"dirty"`
	BuildDate *string `json:"build_date"`
	OS        *string `json:"os"`
	Arch      *string `json:"arch"`
}

func (c CLI) doctorCommand(args []string, emitter *eventEmitter) (commandResult, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(emitter.flagOut)
	if err := flags.Parse(args); err != nil {
		return commandResult{}, invalidDoctorArguments(c, err)
	}
	if flags.NArg() != 0 {
		return commandResult{}, invalidDoctorArguments(c, errors.New("unexpected positional arguments"))
	}

	report := c.collectDoctorReport(context.Background())
	if !emitter.enabled {
		writeHumanDoctorReport(c, report)
	}
	if report.Status == "error" {
		return commandResult{}, &codedError{
			Code:     "health_check_failed",
			ExitCode: 1,
			Err:      errors.New(c.tr("doctor.failed")),
			Details:  report,
		}
	}
	return commandResult{Code: "health_check_complete", Data: report}, nil
}

func invalidDoctorArguments(c CLI, err error) *codedError {
	return &codedError{
		Code:     "invalid_arguments",
		ExitCode: 2,
		Err:      fmt.Errorf(c.tr("error.invalidArguments"), err),
	}
}

func (c CLI) collectDoctorReport(ctx context.Context) doctorReport {
	deps := c.resolvedDoctorDependencies()
	report := doctorReport{Checks: []doctorCheck{}, Homes: []string{}}
	add := func(level, name, code, detail string) {
		report.Checks = append(report.Checks, doctorCheck{Level: level, Name: name, Code: code, Detail: detail})
	}

	paths, err := deps.ResolvePaths()
	if err != nil {
		add("error", "state_directory", "path_resolution_failed", err.Error())
		report.Status = doctorStatus(report.Checks)
		return report
	}
	report.Paths = newDoctorPaths(paths)

	cfg, err := deps.LoadConfig(paths)
	if err != nil {
		add("error", "config", "config_load_failed", err.Error())
		report.Status = doctorStatus(report.Checks)
		return report
	}
	add("ok", "config", "config_loaded", paths.ConfigPath)

	homes, err := deps.ResolveHomes(cfg)
	if err != nil {
		add("error", "codex_home", "home_resolution_failed", err.Error())
		report.Status = doctorStatus(report.Checks)
		return report
	}
	report.Homes = deterministicAbsolutePaths(homes)

	if cfg.ListenAddress == "127.0.0.1" || cfg.ListenAddress == "localhost" {
		add("ok", "loopback", "loopback_only", c.tr("doctor.loopbackOK", cfg.ListenAddress, cfg.Port))
	} else {
		add("error", "loopback", "non_loopback_address", c.tr("doctor.loopbackError"))
	}

	if markerErr := deps.EnsureStateMarker(paths); markerErr != nil {
		add("warning", "state_directory", "state_marker_warning", markerErr.Error())
	} else {
		add("ok", "state_directory", "state_marker_valid", paths.StateDir)
	}
	if permissionErr := deps.LockDown(paths.StateDir); permissionErr != nil {
		add("warning", "state_directory", "permissions_warning", permissionErr.Error())
	} else {
		add("ok", "state_directory", "permissions_restricted", c.tr("doctor.permissions"))
	}

	st, dbErr := store.Open(paths.Database)
	if dbErr != nil {
		add("error", "database", "database_open_failed", dbErr.Error())
	} else {
		status, statusErr := st.Status(ctx)
		if statusErr != nil {
			add("error", "database", "database_status_failed", statusErr.Error())
		} else {
			add("ok", "machine", "machine_identity_loaded", fmt.Sprintf("%s / %s (%s/%s)", status.Machine.Label, status.Machine.ID, status.Machine.OS, status.Machine.Arch))
			if currentHostname, hostErr := os.Hostname(); hostErr == nil && currentHostname != "" &&
				!strings.EqualFold(currentHostname, status.Machine.Hostname) {
				add("warning", "machine_identity", "hostname_mismatch", c.tr("doctor.machineHost", status.Machine.Hostname, currentHostname))
			}
			if status.Machine.OS != runtime.GOOS || status.Machine.Arch != runtime.GOARCH {
				add("warning", "machine_identity", "platform_mismatch", c.tr("doctor.machinePlatform", status.Machine.OS, status.Machine.Arch, runtime.GOOS, runtime.GOARCH))
			}
			add("ok", "database", "database_ready", c.tr("doctor.database", paths.Database, status.EventCount, status.SessionCount))
			add("ok", "accounting", "jsonl_only", c.tr("doctor.jsonlOnly"))
			if status.WarningCount > 0 {
				add("warning", "coverage", "coverage_warning", c.tr("doctor.coverage", status.WarningCount))
			}
		}
		if closeCheck := doctorStoreCloseCheck(st.Close()); closeCheck != nil {
			report.Checks = append(report.Checks, *closeCheck)
		}
	}

	sessionHomes := map[string]string{}
	for _, home := range report.Homes {
		info, statErr := os.Stat(home)
		if statErr != nil || !info.IsDir() {
			add("warning", "codex_home", "home_unreadable", c.tr("doctor.homeUnreadable", home))
			continue
		}
		discovery := usage.DiscoverHome(ctx, home)
		if discovery.Warning != "" {
			add("warning", "state_database", "state_database_warning", c.tr("doctor.stateWarning", home, discovery.Warning, len(discovery.Paths)))
		} else if discovery.StateDB != "" && !discovery.Fallback {
			add("ok", "state_database", "state_database_ready", c.tr("doctor.stateDB", discovery.StateDB, len(discovery.Paths)))
		} else {
			add("warning", "state_database", "state_database_missing", c.tr("doctor.stateMissing", home, len(discovery.Paths)))
		}
		for _, session := range discovery.Sessions {
			if previous, exists := sessionHomes[session.SessionID]; exists && !sameFilePath(previous, home) {
				add("warning", "shared_history", "shared_session_history", c.tr("doctor.sharedHistory", session.SessionID, previous, home))
				break
			}
			sessionHomes[session.SessionID] = home
		}
		legacyManaged, legacyErr := config.HasLegacyManagedOTel(home)
		if legacyErr != nil {
			add("warning", "codex_config", "config_unreadable", legacyErr.Error())
		} else if legacyManaged {
			add("warning", "codex_config", "legacy_managed_config", c.tr("doctor.legacyManaged", home))
		} else {
			add("ok", "codex_config", "config_untouched", c.tr("doctor.configUntouched", home))
		}
		if runtime.GOOS == "linux" && strings.Contains(filepath.ToSlash(home), "/mnt/") {
			add("warning", "shared_home", "cross_system_mount", c.tr("doctor.sharedHome", home))
		}
	}

	expected, identityErr := currentBuildIdentity()
	if identityErr != nil {
		add("error", "service_identity", "build_metadata_invalid", identityErr.Error())
	} else {
		endpoint := deps.HealthEndpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		probeErr := probeIdentityWithClient(probeCtx, deps.HTTPClient, endpoint, expected)
		cancel()
		if probeErr != nil {
			add("error", "service_identity", doctorProbeCode(probeErr), c.tr("doctor.identityError", probeErr))
		} else {
			add("ok", "service_identity", "identity_match", c.tr("doctor.identityOK", expected.Version, expected.Commit, expected.OS, expected.Arch))
		}
	}
	add("ok", "privacy_schema", "privacy_schema_safe", c.tr("doctor.privacy"))
	add("ok", "network", "loopback_no_telemetry", c.tr("doctor.network"))

	report.Status = doctorStatus(report.Checks)
	return report
}

func (c CLI) resolvedDoctorDependencies() doctorDependencies {
	deps := doctorDependencies{
		ResolvePaths:      config.ResolvePaths,
		LoadConfig:        config.Load,
		ResolveHomes:      config.CodexHomes,
		EnsureStateMarker: config.EnsureStateMarker,
		LockDown:          platform.LockDown,
		HTTPClient:        newLoopbackHealthClient(),
	}
	if c.doctorDeps == nil {
		return deps
	}
	if c.doctorDeps.ResolvePaths != nil {
		deps.ResolvePaths = c.doctorDeps.ResolvePaths
	}
	if c.doctorDeps.LoadConfig != nil {
		deps.LoadConfig = c.doctorDeps.LoadConfig
	}
	if c.doctorDeps.ResolveHomes != nil {
		deps.ResolveHomes = c.doctorDeps.ResolveHomes
	}
	if c.doctorDeps.EnsureStateMarker != nil {
		deps.EnsureStateMarker = c.doctorDeps.EnsureStateMarker
	}
	if c.doctorDeps.LockDown != nil {
		deps.LockDown = c.doctorDeps.LockDown
	}
	if c.doctorDeps.HTTPClient != nil {
		deps.HTTPClient = c.doctorDeps.HTTPClient
	}
	deps.HealthEndpoint = c.doctorDeps.HealthEndpoint
	return deps
}

func newDoctorPaths(paths config.Paths) doctorPaths {
	return doctorPaths{
		StateDir: absolutePath(paths.StateDir), ConfigPath: absolutePath(paths.ConfigPath),
		DatabasePath: absolutePath(paths.Database), InstallDir: absolutePath(paths.InstallDir),
		Executable: absolutePath(paths.InstalledEXE),
	}
}

func absolutePath(path string) string {
	if path == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(absolute)
}

func deterministicAbsolutePaths(paths []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = absolutePath(path)
		if path == "" {
			continue
		}
		key := path
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func doctorStatus(checks []doctorCheck) string {
	status := "ok"
	for _, check := range checks {
		switch check.Level {
		case "error":
			return "error"
		case "warning":
			status = "warning"
		}
	}
	return status
}

func doctorStoreCloseCheck(err error) *doctorCheck {
	if err == nil {
		return nil
	}
	return &doctorCheck{
		Level: "error", Name: "database", Code: "database_close_failed", Detail: err.Error(),
	}
}

func writeHumanDoctorReport(c CLI, report doctorReport) {
	for _, item := range report.Checks {
		symbol := map[string]string{"ok": "✓", "warning": "!", "error": "✗"}[item.Level]
		fmt.Fprintf(c.Stdout, "%s %-20s %s\n", symbol, doctorHumanLabel(c, item.Name), item.Detail)
	}
}

func doctorHumanLabel(c CLI, name string) string {
	key := ""
	switch name {
	case "state_directory":
		key = "doctor.n.stateDir"
	case "config":
		key = "doctor.n.config"
	case "codex_home":
		key = "doctor.n.codexHome"
	case "loopback":
		key = "doctor.n.loopback"
	case "database":
		key = "doctor.n.database"
	case "machine":
		key = "doctor.n.machine"
	case "machine_identity":
		key = "doctor.n.machineID"
	case "accounting":
		key = "doctor.n.accounting"
	case "coverage":
		key = "doctor.n.coverage"
	case "state_database":
		key = "doctor.n.stateDB"
	case "shared_history":
		key = "doctor.n.history"
	case "codex_config":
		key = "doctor.n.codexConfig"
	case "shared_home":
		key = "doctor.n.sharedHome"
	case "service_identity":
		key = "doctor.n.serviceID"
	case "privacy_schema":
		key = "doctor.n.privacy"
	case "network":
		key = "doctor.n.network"
	default:
		return name
	}
	return c.tr(key)
}

func doctorProbeCode(err error) string {
	var probe *identityProbeError
	if !errors.As(err, &probe) {
		return "service_unavailable"
	}
	if probe.Code == "health_identity_mismatch" {
		return "identity_mismatch"
	}
	return probe.Code
}

func newLoopbackHealthClient() *http.Client {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	return &http.Client{
		Timeout: healthRequestTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				connection, err := dialer.DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				remote, ok := connection.RemoteAddr().(*net.TCPAddr)
				if !ok || remote.IP == nil || !remote.IP.IsLoopback() {
					_ = connection.Close()
					return nil, fmt.Errorf("health endpoint resolved outside loopback: %s", connection.RemoteAddr())
				}
				return connection, nil
			},
		},
	}
}

func probeIdentity(ctx context.Context, baseURL string, expected buildIdentity) error {
	return probeIdentityWithClient(ctx, newLoopbackHealthClient(), baseURL, expected)
}

func probeIdentityWithClient(ctx context.Context, client *http.Client, baseURL string, expected buildIdentity) error {
	if ctx == nil {
		return newIdentityProbeError("health_request_failed", "", errors.New("nil context"))
	}
	endpoint, err := parseHealthEndpoint(baseURL)
	if err != nil {
		return newIdentityProbeError("health_endpoint_invalid", "", err)
	}
	if client == nil {
		client = newLoopbackHealthClient()
	}
	if err := validateProbeIdentityShape(expected); err != nil {
		return newIdentityProbeError("health_expected_identity_invalid", "", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return newIdentityProbeError("health_request_failed", "", err)
	}
	redirectRefused := errors.New("health redirects are forbidden")
	probeClient := *client
	if probeClient.Timeout == 0 {
		probeClient.Timeout = healthRequestTimeout
	}
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return redirectRefused
	}
	response, err := probeClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(err, redirectRefused) {
			return newIdentityProbeError("health_redirect_forbidden", "", err)
		}
		return newIdentityProbeError("health_request_failed", "", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return newIdentityProbeError("health_http_status", "", fmt.Errorf("status %d", response.StatusCode))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return newIdentityProbeError("health_content_type", "", fmt.Errorf("content type %q", response.Header.Get("Content-Type")))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, healthResponseLimit+1))
	if err != nil {
		return newIdentityProbeError("health_request_failed", "", err)
	}
	if int64(len(body)) > healthResponseLimit {
		return newIdentityProbeError("health_response_too_large", "", fmt.Errorf("response exceeds %d bytes", healthResponseLimit))
	}
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return newIdentityProbeError("health_response_invalid", "", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var payload healthIdentityPayload
	if err := decoder.Decode(&payload); err != nil {
		return newIdentityProbeError("health_response_invalid", "", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return newIdentityProbeError("health_response_invalid", "", err)
	}
	actual, ok, err := payload.identity()
	if err != nil {
		return newIdentityProbeError("health_response_invalid", "", err)
	}
	if !ok {
		return newIdentityProbeError("health_not_ok", "ok", errors.New("service reported ok=false"))
	}
	for _, comparison := range []struct {
		field    string
		actual   any
		expected any
	}{
		{field: "version", actual: actual.Version, expected: expected.Version},
		{field: "commit", actual: actual.Commit, expected: expected.Commit},
		{field: "dirty", actual: actual.Dirty, expected: expected.Dirty},
		{field: "build_date", actual: actual.BuildDate, expected: expected.BuildDate},
		{field: "os", actual: actual.OS, expected: expected.OS},
		{field: "arch", actual: actual.Arch, expected: expected.Arch},
	} {
		if comparison.actual != comparison.expected {
			return newIdentityProbeError("health_identity_mismatch", comparison.field,
				fmt.Errorf("got %v, want %v", comparison.actual, comparison.expected))
		}
	}
	return nil
}

func parseHealthEndpoint(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("health base URL must use http://127.0.0.1 with an explicit port")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("health base URL path must be empty or /")
	}
	if parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("health base URL cannot contain escaped paths, query, or fragment")
	}
	portText := parsed.Port()
	if portText == "" {
		return "", errors.New("health base URL requires an explicit port")
	}
	for index := 0; index < len(portText); index++ {
		if portText[index] < '0' || portText[index] > '9' {
			return "", fmt.Errorf("invalid health port %q", portText)
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid health port %q", portText)
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Path:   "/healthz",
	}).String(), nil
}

func (payload healthIdentityPayload) identity() (buildIdentity, bool, error) {
	if payload.OK == nil || payload.Version == nil || payload.Commit == nil || payload.Dirty == nil ||
		payload.BuildDate == nil || payload.OS == nil || payload.Arch == nil {
		return buildIdentity{}, false, errors.New("health response has missing or null fields")
	}
	identity := buildIdentity{
		Version: *payload.Version, Commit: *payload.Commit, Dirty: *payload.Dirty,
		BuildDate: *payload.BuildDate, OS: *payload.OS, Arch: *payload.Arch,
	}
	if err := validateProbeIdentityShape(identity); err != nil {
		return buildIdentity{}, false, err
	}
	return identity, *payload.OK, nil
}

func validateProbeIdentityShape(identity buildIdentity) error {
	for field, value := range map[string]string{
		"version": identity.Version, "commit": identity.Commit, "build_date": identity.BuildDate,
		"os": identity.OS, "arch": identity.Arch,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", field)
		}
	}
	return nil
}

func validateTrustedReleaseIdentity(identity buildIdentity) error {
	if !isStableVersion(identity.Version) {
		return fmt.Errorf("invalid trusted version %q", identity.Version)
	}
	if len(identity.Commit) != 40 {
		return fmt.Errorf("trusted commit must be 40 lowercase hexadecimal characters")
	}
	nonZeroCommit := false
	for _, character := range identity.Commit {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return fmt.Errorf("trusted commit must be 40 lowercase hexadecimal characters")
		}
		if character != '0' {
			nonZeroCommit = true
		}
	}
	if !nonZeroCommit {
		return errors.New("trusted commit cannot be all zero")
	}
	if identity.Dirty {
		return errors.New("trusted identity cannot be dirty")
	}
	buildDate, err := time.Parse(time.RFC3339, identity.BuildDate)
	if err != nil || buildDate.IsZero() {
		return fmt.Errorf("invalid trusted build date %q", identity.BuildDate)
	}
	if identity.OS != "windows" && identity.OS != "linux" {
		return fmt.Errorf("unsupported trusted os %q", identity.OS)
	}
	if identity.Arch != "amd64" && identity.Arch != "arm64" {
		return fmt.Errorf("unsupported trusted arch %q", identity.Arch)
	}
	return nil
}

func isStableVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for index := 0; index < len(part); index++ {
			if part[index] < '0' || part[index] > '9' {
				return false
			}
		}
		if len(part) > 1 && part[0] == '0' {
			return false
		}
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func rejectDuplicateJSONKeys(value []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = true
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("invalid JSON object closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("invalid JSON array closing delimiter")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("health response has trailing JSON")
	}
	return err
}

func newIdentityProbeError(code, field string, err error) *identityProbeError {
	return &identityProbeError{Code: code, Field: field, Err: err}
}
