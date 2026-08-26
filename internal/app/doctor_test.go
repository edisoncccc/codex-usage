package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zJay26/codex-usage/internal/cliui"
	"github.com/zJay26/codex-usage/internal/config"
)

func TestHealthProbeRejectsVersionOrCommitMismatch(t *testing.T) {
	expected := buildIdentity{
		Version:   "2.3.6",
		Commit:    strings.Repeat("a", 40),
		Dirty:     false,
		BuildDate: "2026-08-26T00:00:00Z",
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}

	t.Run("loopback exact match", func(t *testing.T) {
		server := healthFixtureServer(t, http.StatusOK, "application/json", healthPayload(expected))
		defer server.Close()
		if err := probeIdentityWithClient(context.Background(), server.Client(), server.URL+"/", expected); err != nil {
			t.Fatalf("exact loopback identity was rejected: %v", err)
		}
	})

	t.Run("invalid endpoint before network", func(t *testing.T) {
		for _, endpoint := range []string{
			"http://192.0.2.1:43189",
			"http://127.0.0.2:43189",
			"http://localhost:43189",
			"http://example.test:43189",
			"http://[::1]:43189",
			"https://127.0.0.1:43189",
			"http://user@127.0.0.1:43189",
			"http://127.0.0.1:43189?query=1",
			"http://127.0.0.1:43189#fragment",
			"http://127.0.0.1:43189/path",
			"http://127.0.0.1",
			"http://127.0.0.1:0",
			"http://127.0.0.1:65536",
			"http://127.0.0.1:not-a-port",
		} {
			t.Run(endpoint, func(t *testing.T) {
				attempts := 0
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts++
					return nil, errors.New("network must not be reached")
				})}
				err := probeIdentityWithClient(context.Background(), client, endpoint, expected)
				assertIdentityProbeError(t, err, "health_endpoint_invalid", "")
				if attempts != 0 {
					t.Fatalf("endpoint %q performed %d network attempts", endpoint, attempts)
				}
			})
		}
	})

	t.Run("redirect forbidden", func(t *testing.T) {
		target := healthFixtureServer(t, http.StatusOK, "application/json", healthPayload(expected))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer redirect.Close()
		err := probeIdentityWithClient(context.Background(), redirect.Client(), redirect.URL, expected)
		assertIdentityProbeError(t, err, "health_redirect_forbidden", "")
	})

	for _, stage := range []string{"header", "body"} {
		t.Run("client timeout during "+stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "body" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"ok":`))
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 75 * time.Millisecond
			started := time.Now()
			err := probeIdentityWithClient(context.Background(), client, server.URL, expected)
			assertIdentityProbeError(t, err, "health_request_failed", "")
			if elapsed := time.Since(started); elapsed > 2*time.Second {
				t.Fatalf("%s timeout took %s", stage, elapsed)
			}
		})
	}

	t.Run("ok false", func(t *testing.T) {
		payload := healthPayload(expected)
		payload["ok"] = false
		server := healthFixtureServer(t, http.StatusOK, "application/json", payload)
		defer server.Close()
		err := probeIdentityWithClient(context.Background(), server.Client(), server.URL, expected)
		assertIdentityProbeError(t, err, "health_not_ok", "ok")
	})

	for _, test := range []struct {
		name  string
		field string
		edit  func(map[string]any)
	}{
		{name: "version", field: "version", edit: func(value map[string]any) { value["version"] = "2.3.7" }},
		{name: "commit", field: "commit", edit: func(value map[string]any) { value["commit"] = strings.Repeat("b", 40) }},
		{name: "dirty", field: "dirty", edit: func(value map[string]any) { value["dirty"] = true }},
		{name: "build date", field: "build_date", edit: func(value map[string]any) { value["build_date"] = "2026-08-27T00:00:00Z" }},
		{name: "os", field: "os", edit: func(value map[string]any) { value["os"] = "mismatch" }},
		{name: "arch", field: "arch", edit: func(value map[string]any) { value["arch"] = "mismatch" }},
	} {
		t.Run("mismatch "+test.name, func(t *testing.T) {
			payload := healthPayload(expected)
			test.edit(payload)
			server := healthFixtureServer(t, http.StatusOK, "application/json", payload)
			defer server.Close()
			err := probeIdentityWithClient(context.Background(), server.Client(), server.URL, expected)
			assertIdentityProbeError(t, err, "health_identity_mismatch", test.field)
		})
	}

	invalidBodies := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"ok":true,"version":"2.3.6","commit":"` + strings.Repeat("a", 40) + `","dirty":false,"build_date":"2026-08-26T00:00:00Z","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","extra":true}`},
		{name: "duplicate field", body: `{"ok":true,"ok":true,"version":"2.3.6","commit":"` + strings.Repeat("a", 40) + `","dirty":false,"build_date":"2026-08-26T00:00:00Z","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `"}`},
		{name: "trailing value", body: `{"ok":true,"version":"2.3.6","commit":"` + strings.Repeat("a", 40) + `","dirty":false,"build_date":"2026-08-26T00:00:00Z","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `"} {}`},
		{name: "missing field", body: `{"ok":true,"version":"2.3.6","commit":"` + strings.Repeat("a", 40) + `","dirty":false,"build_date":"2026-08-26T00:00:00Z","os":"` + runtime.GOOS + `"}`},
		{name: "null field", body: `{"ok":true,"version":null,"commit":"` + strings.Repeat("a", 40) + `","dirty":false,"build_date":"2026-08-26T00:00:00Z","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `"}`},
	}
	for _, test := range invalidBodies {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			err := probeIdentityWithClient(context.Background(), server.Client(), server.URL, expected)
			assertIdentityProbeError(t, err, "health_response_invalid", "")
		})
	}

	t.Run("body limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", int(healthResponseLimit)) + `"}`))
		}))
		defer server.Close()
		err := probeIdentityWithClient(context.Background(), server.Client(), server.URL, expected)
		assertIdentityProbeError(t, err, "health_response_too_large", "")
	})

	for _, test := range []struct {
		name        string
		status      int
		contentType string
		wantCode    string
	}{
		{name: "status", status: http.StatusServiceUnavailable, contentType: "application/json", wantCode: "health_http_status"},
		{name: "content type", status: http.StatusOK, contentType: "text/plain", wantCode: "health_content_type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := healthFixtureServer(t, test.status, test.contentType, healthPayload(expected))
			defer server.Close()
			err := probeIdentityWithClient(context.Background(), server.Client(), server.URL, expected)
			assertIdentityProbeError(t, err, test.wantCode, "")
		})
	}

	t.Run("context cancellation", func(t *testing.T) {
		server := healthFixtureServer(t, http.StatusOK, "application/json", healthPayload(expected))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := probeIdentityWithClient(ctx, server.Client(), server.URL, expected)
		assertIdentityProbeError(t, err, "health_request_failed", "")
	})

	t.Run("source build exact identity", func(t *testing.T) {
		dev := buildIdentity{Version: "2.3.5", Commit: "dev", Dirty: true, BuildDate: "unknown", OS: runtime.GOOS, Arch: runtime.GOARCH}
		server := healthFixtureServer(t, http.StatusOK, "application/json", healthPayload(dev))
		defer server.Close()
		if err := probeIdentity(context.Background(), server.URL, dev); err != nil {
			t.Fatalf("exact source identity was rejected: %v", err)
		}
	})

	validTrusted := expected
	if err := validateTrustedReleaseIdentity(validTrusted); err != nil {
		t.Fatalf("valid trusted identity: %v", err)
	}
	t.Run("trusted version syntax", func(t *testing.T) {
		for _, test := range []struct {
			version string
			valid   bool
		}{
			{version: "2.3.6", valid: true},
			{version: "0.0.0", valid: true},
			{version: "02.3.6"},
			{version: "2.03.6"},
			{version: "2.3.06"},
			{version: "v2.3.6"},
			{version: "2.3.6-rc.1"},
			{version: "2.x.6"},
			{version: "2..6"},
			{version: "+2.3.6"},
			{version: "2.+3.6"},
			{version: "２.3.6"},
			{version: "18446744073709551616.3.6"},
			{version: ""},
		} {
			t.Run(test.version, func(t *testing.T) {
				identity := validTrusted
				identity.Version = test.version
				err := validateTrustedReleaseIdentity(identity)
				if (err == nil) != test.valid {
					t.Fatalf("version=%q valid=%t error=%v", test.version, test.valid, err)
				}
			})
		}
	})
	for _, test := range []struct {
		name string
		edit func(*buildIdentity)
	}{
		{name: "short commit", edit: func(value *buildIdentity) { value.Commit = "abc" }},
		{name: "uppercase commit", edit: func(value *buildIdentity) { value.Commit = strings.Repeat("A", 40) }},
		{name: "zero commit", edit: func(value *buildIdentity) { value.Commit = strings.Repeat("0", 40) }},
		{name: "dirty", edit: func(value *buildIdentity) { value.Dirty = true }},
		{name: "empty version", edit: func(value *buildIdentity) { value.Version = "" }},
		{name: "invalid version", edit: func(value *buildIdentity) { value.Version = "v2.3.6" }},
		{name: "invalid build date", edit: func(value *buildIdentity) { value.BuildDate = "unknown" }},
		{name: "unsupported os", edit: func(value *buildIdentity) { value.OS = "darwin" }},
		{name: "unsupported arch", edit: func(value *buildIdentity) { value.Arch = "386" }},
	} {
		t.Run("trusted "+test.name, func(t *testing.T) {
			identity := validTrusted
			test.edit(&identity)
			if err := validateTrustedReleaseIdentity(identity); err == nil {
				t.Fatalf("trusted identity was accepted: %+v", identity)
			}
		})
	}
}

func TestDoctorHumanOutputUsesLocalizedLabels(t *testing.T) {
	report := doctorReport{Checks: []doctorCheck{
		{Level: "ok", Name: "state_directory", Code: "state_marker_valid", Detail: "fixture"},
		{Level: "error", Name: "service_identity", Code: "identity_mismatch", Detail: "fixture"},
	}}
	for _, test := range []struct {
		name   string
		locale string
		want   []string
	}{
		{name: "Chinese", locale: "zh-CN", want: []string{"状态目录", "服务身份"}},
		{name: "English", locale: "en", want: []string{"State directory", "Service identity"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			locale, err := cliui.Detect(test.locale, "", "")
			if err != nil {
				t.Fatal(err)
			}
			writeHumanDoctorReport(CLI{Stdout: &stdout, locale: locale}, report)
			output := stdout.String()
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Fatalf("output %q does not contain localized label %q", output, want)
				}
			}
			for _, machineName := range []string{"state_directory", "service_identity"} {
				if strings.Contains(output, machineName) {
					t.Fatalf("human output leaked machine name %q: %q", machineName, output)
				}
			}
		})
	}
}

func TestDoctorStoreCloseErrorUsesStableCode(t *testing.T) {
	if check := doctorStoreCloseCheck(nil); check != nil {
		t.Fatalf("successful close produced a check: %#v", check)
	}
	check := doctorStoreCloseCheck(errors.New("fixture close failure"))
	if check == nil || check.Level != "error" || check.Name != "database" ||
		check.Code != "database_close_failed" || check.Detail != "fixture close failure" {
		t.Fatalf("close error check=%#v", check)
	}
}

func TestDoctorJSONUsesStableSnakeCaseFields(t *testing.T) {
	fixture := newDoctorFixture(t, nil)
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout, Stderr: &stderr, Now: fixture.now,
		doctorDeps: fixture.dependencies,
	}).Run([]string{"doctor", "--json"})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 || events[0]["event"] != "result" || events[0]["code"] != "health_check_complete" {
		t.Fatalf("events=%#v", events)
	}
	report := doctorResultMap(t, events[0])
	if report["status"] != "warning" {
		t.Fatalf("status=%#v report=%#v", report["status"], report)
	}
	paths, ok := report["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths=%#v", report["paths"])
	}
	wantPaths := map[string]string{
		"state_dir": fixture.paths.StateDir, "config_path": fixture.paths.ConfigPath,
		"database_path": fixture.paths.Database, "install_dir": fixture.paths.InstallDir,
		"executable": fixture.paths.InstalledEXE,
	}
	if len(paths) != len(wantPaths) {
		t.Fatalf("paths=%#v", paths)
	}
	for key, value := range wantPaths {
		if !filepath.IsAbs(paths[key].(string)) || paths[key] != value {
			t.Fatalf("paths[%q]=%#v want=%q", key, paths[key], value)
		}
	}

	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	checks, ok := report["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("checks=%#v", report["checks"])
	}
	for _, raw := range checks {
		check := raw.(map[string]any)
		for _, key := range []string{"level", "name", "code"} {
			if value, ok := check[key].(string); !ok || value == "" {
				t.Fatalf("check field %q invalid: %#v", key, check)
			}
		}
		if len(check) > 4 {
			t.Fatalf("check has unstable fields: %#v", check)
		}
		if !snake.MatchString(check["name"].(string)) || !snake.MatchString(check["code"].(string)) {
			t.Fatalf("check is not stable snake_case: %#v", check)
		}
		switch check["level"] {
		case "ok", "warning", "error":
		default:
			t.Fatalf("unstable check level: %#v", check)
		}
	}

	homes, ok := report["homes"].([]any)
	if !ok || len(homes) != 2 || homes[0].(string) > homes[1].(string) {
		t.Fatalf("homes are not deterministic: %#v", report["homes"])
	}
}

func TestDoctorJSONIsLocaleIndependent(t *testing.T) {
	fixture := newDoctorFixture(t, nil)
	var stable [][][3]string
	for _, language := range []string{"zh-CN", "en"} {
		var stdout, stderr bytes.Buffer
		exitCode := (CLI{
			Stdout: &stdout, Stderr: &stderr, Now: fixture.now,
			doctorDeps: fixture.dependencies,
		}).Run([]string{"--lang", language, "doctor", "--json"})
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("language=%s exit=%d stderr=%q", language, exitCode, stderr.String())
		}
		events := decodeMachineEvents(t, stdout.String())
		if len(events) != 1 {
			t.Fatalf("language=%s events=%#v", language, events)
		}
		stable = append(stable, stableDoctorChecks(t, doctorResultMap(t, events[0])))
	}
	if !reflect.DeepEqual(stable[0], stable[1]) {
		t.Fatalf("stable doctor fields changed with locale:\nzh=%#v\nen=%#v", stable[0], stable[1])
	}
}

func TestDoctorJSONErrorCheckReturnsNonZero(t *testing.T) {
	fixture := newDoctorFixture(t, func(payload map[string]any) { payload["commit"] = strings.Repeat("b", 40) })
	var stdout, stderr bytes.Buffer
	exitCode := (CLI{
		Stdout: &stdout, Stderr: &stderr, Now: fixture.now,
		doctorDeps: fixture.dependencies,
	}).Run([]string{"doctor", "--json"})
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	events := decodeMachineEvents(t, stdout.String())
	if len(events) != 1 || events[0]["event"] != "error" || events[0]["code"] != "health_check_failed" {
		t.Fatalf("events=%#v", events)
	}
	report := doctorResultMap(t, events[0])
	if report["status"] != "error" {
		t.Fatalf("report=%#v", report)
	}
	found := false
	for _, check := range report["checks"].([]any) {
		fields := check.(map[string]any)
		if fields["name"] == "service_identity" && fields["level"] == "error" && fields["code"] == "identity_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("service identity error missing: %#v", report["checks"])
	}

	for _, args := range [][]string{{"doctor", "--json", "--unknown"}, {"doctor", "--json", "extra"}} {
		stdout.Reset()
		stderr.Reset()
		exitCode = (CLI{Stdout: &stdout, Stderr: &stderr, doctorDeps: fixture.dependencies}).Run(args)
		events = decodeMachineEvents(t, stdout.String())
		if exitCode != 2 || stderr.Len() != 0 || len(events) != 1 || events[0]["code"] != "invalid_arguments" {
			t.Fatalf("args=%v exit=%d stderr=%q events=%#v", args, exitCode, stderr.String(), events)
		}
	}
}

type doctorFixture struct {
	paths        config.Paths
	dependencies *doctorDependencies
	now          func() time.Time
}

func newDoctorFixture(t *testing.T, editHealth func(map[string]any)) doctorFixture {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{
		StateDir: root, ConfigPath: filepath.Join(root, "config.json"),
		Database: filepath.Join(root, "usage.sqlite"), InstallDir: filepath.Join(root, "bin"),
		InstalledEXE: filepath.Join(root, "bin", "codex-usage.exe"),
	}
	homes := []string{filepath.Join(root, "home-b"), filepath.Join(root, "home-a")}
	for _, home := range homes {
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	identity := buildIdentity{
		Version: "2.3.6", Commit: strings.Repeat("a", 40), Dirty: false,
		BuildDate: "2026-08-26T00:00:00Z", OS: runtime.GOOS, Arch: runtime.GOARCH,
	}
	restoreBuildMetadata(t, identity.Version, identity.Commit, "false", identity.BuildDate)
	payload := healthPayload(identity)
	if editHealth != nil {
		editHealth(payload)
	}
	healthServer := healthFixtureServer(t, http.StatusOK, "application/json", payload)
	t.Cleanup(healthServer.Close)
	dependencies := &doctorDependencies{
		ResolvePaths: func() (config.Paths, error) { return paths, nil },
		LoadConfig: func(config.Paths) (config.Config, error) {
			return config.Config{ListenAddress: "127.0.0.1", Port: config.DefaultPort, ScanIntervalSeconds: 600}, nil
		},
		ResolveHomes:      func(config.Config) ([]string, error) { return append([]string(nil), homes...), nil },
		EnsureStateMarker: func(config.Paths) error { return nil },
		LockDown:          func(string) error { return nil },
		HTTPClient:        healthServer.Client(),
		HealthEndpoint:    healthServer.URL,
	}
	return doctorFixture{
		paths: paths, dependencies: dependencies,
		now: func() time.Time { return time.Date(2026, time.August, 26, 1, 2, 3, 0, time.UTC) },
	}
}

func healthPayload(identity buildIdentity) map[string]any {
	return map[string]any{
		"ok": true, "version": identity.Version, "commit": identity.Commit,
		"dirty": identity.Dirty, "build_date": identity.BuildDate,
		"os": identity.OS, "arch": identity.Arch,
	}
}

func healthFixtureServer(t *testing.T, status int, contentType string, payload any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode health fixture: %v", err)
		}
	}))
}

func assertIdentityProbeError(t *testing.T, err error, code, field string) {
	t.Helper()
	var typed *identityProbeError
	if !errors.As(err, &typed) || typed.Code != code || typed.Field != field {
		t.Fatalf("error=%T %v, want code=%q field=%q", err, err, code, field)
	}
}

func doctorResultMap(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	result, ok := event["result"].(map[string]any)
	if !ok {
		t.Fatalf("result=%#v event=%#v", event["result"], event)
	}
	return result
}

func stableDoctorChecks(t *testing.T, report map[string]any) [][3]string {
	t.Helper()
	raw, ok := report["checks"].([]any)
	if !ok {
		t.Fatalf("checks=%#v", report["checks"])
	}
	result := make([][3]string, 0, len(raw))
	for _, value := range raw {
		check := value.(map[string]any)
		result = append(result, [3]string{check["level"].(string), check["name"].(string), check["code"].(string)})
	}
	return result
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
