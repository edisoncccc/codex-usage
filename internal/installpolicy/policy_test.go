package installpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const stableTagPattern = `^v[0-9]+\.[0-9]+\.[0-9]+$`

func TestRepositoryPolicyIsSourceOnly(t *testing.T) {
	policy := loadRepositoryPolicy(t)

	if policy.SchemaVersion != "1" {
		t.Fatalf("schema_version = %q, want 1", policy.SchemaVersion)
	}
	wantRepository := Repository{
		Host:  "github.com",
		Owner: "edisoncccc",
		Name:  "codex-usage",
		URL:   "https://github.com/edisoncccc/codex-usage",
	}
	if policy.CanonicalRepository != wantRepository {
		t.Fatalf("canonical_repository = %#v, want %#v", policy.CanonicalRepository, wantRepository)
	}
	if policy.StableTagPattern != stableTagPattern {
		t.Fatalf("stable_tag_pattern = %q, want %q", policy.StableTagPattern, stableTagPattern)
	}
	if _, err := regexp.Compile(policy.StableTagPattern); err != nil {
		t.Fatalf("stable_tag_pattern does not compile: %v", err)
	}
	if BinaryReleaseEnabled {
		t.Fatal("compiled binary release channel must remain disabled")
	}
	if policy.BinaryReleaseEnabled {
		t.Fatal("repository policy enables binary releases")
	}

	wantRelease := ReleaseRequirements{
		Source:                     "github_release",
		MustBeImmutable:            true,
		AllowDraft:                 false,
		AllowPrerelease:            false,
		RequireSHA256Sums:          true,
		RequireAssetDigest:         true,
		RequireArtifactAttestation: true,
	}
	if policy.Release != wantRelease {
		t.Fatalf("release requirements = %#v, want %#v", policy.Release, wantRelease)
	}
	wantWindows := WindowsRequirements{
		RequireAuthenticode: true,
		RequireTimestamp:    true,
		PublisherSubject:    nil,
	}
	if policy.Windows != wantWindows {
		t.Fatalf("windows requirements = %#v, want %#v", policy.Windows, wantWindows)
	}

	wantInstallation := InstallationPolicy{
		Scope:                     "current_user",
		AllowElevation:            false,
		AllowSilentSourceFallback: false,
		AutomaticBackgroundUpdate: false,
		ListenAddress:             "127.0.0.1",
		Windows: PlatformInstall{
			ProgramPath: `%LOCALAPPDATA%\Programs\codex-usage\codex-usage.exe`,
			StatePath:   `%LOCALAPPDATA%\codex-usage`,
			Service:     "HKCU Run",
		},
		Linux: PlatformInstall{
			ProgramPath: "~/.local/bin/codex-usage",
			StatePath:   "${XDG_DATA_HOME:-~/.local/share}/codex-usage",
			Service:     "systemd --user",
		},
	}
	if policy.Installation != wantInstallation {
		t.Fatalf("installation policy = %#v, want %#v", policy.Installation, wantInstallation)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() rejected repository policy: %v", err)
	}
}

func TestRepositoryPolicyMapsEverySupportedAsset(t *testing.T) {
	policy := loadRepositoryPolicy(t)
	want := map[string]string{
		"windows/amd64": "codex-usage-windows-amd64.exe",
		"windows/arm64": "codex-usage-windows-arm64.exe",
		"linux/amd64":   "codex-usage-linux-amd64",
		"linux/arm64":   "codex-usage-linux-arm64",
	}
	if len(policy.Platforms) != len(want) {
		t.Fatalf("platform count = %d, want %d", len(policy.Platforms), len(want))
	}
	seen := make(map[string]bool, len(policy.Platforms))
	for _, platform := range policy.Platforms {
		key := platform.OS + "/" + platform.Arch
		wantAsset, ok := want[key]
		if !ok {
			t.Errorf("unexpected platform %q", key)
			continue
		}
		if seen[key] {
			t.Errorf("duplicate platform %q", key)
		}
		seen[key] = true
		if platform.Asset != wantAsset {
			t.Errorf("asset for %s = %q, want %q", key, platform.Asset, wantAsset)
		}
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("missing platform %q", key)
		}
	}
}

func TestReleaseWorkflowCannotPublishWhileSourceOnly(t *testing.T) {
	policy := loadRepositoryPolicy(t)
	if policy.BinaryReleaseEnabled {
		t.Fatal("test requires the repository policy to be source-only")
	}

	if err := validateSourceOnlyWorkflow(readRepositoryWorkflow(t)); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOnlyWorkflowValidatorRejectsPublishingBypasses(t *testing.T) {
	workflow := strings.ReplaceAll(readRepositoryWorkflow(t), "\r\n", "\n")
	if err := validateSourceOnlyWorkflow(workflow); err != nil {
		t.Fatalf("validator rejected the repository workflow: %v", err)
	}

	extraNamedRun := strings.Replace(
		workflow,
		"      - run: go test ./internal/installpolicy -count=1",
		"      - name: Publish through API\n        run: gh api --method POST repos/example/releases\n      - run: go test ./internal/installpolicy -count=1",
		1,
	)
	jobWritePermission := strings.Replace(
		workflow,
		"  policy:\n",
		"  policy:\n    permissions:\n      contents: write\n",
		1,
	)

	for _, test := range []struct {
		name     string
		workflow string
	}{
		{name: "named extra run step", workflow: extraNamedRun},
		{name: "job write permission", workflow: jobWritePermission},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.workflow == workflow {
				t.Fatal("test setup did not modify the workflow")
			}
			if err := validateSourceOnlyWorkflow(test.workflow); err == nil {
				t.Fatal("validator accepted a publishing bypass")
			}
		})
	}
}

const canonicalSourceOnlyWorkflow = `name: source-only release guard

on:
  workflow_dispatch:

permissions:
  contents: read

jobs:
  policy:
    name: Verify source-only policy
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - run: go test ./internal/installpolicy -count=1`

func validateSourceOnlyWorkflow(workflow string) error {
	normalized := strings.TrimSpace(strings.ReplaceAll(workflow, "\r\n", "\n"))
	if normalized != canonicalSourceOnlyWorkflow {
		return fmt.Errorf("release workflow must exactly match the source-only sentinel")
	}
	return nil
}

func TestParseRejectsMissingRequiredFields(t *testing.T) {
	data := readRepositoryPolicyJSON(t)
	paths := []string{
		"schema_version",
		"canonical_repository",
		"stable_tag_pattern",
		"binary_release_enabled",
		"release",
		"windows",
		"platforms",
		"installation",
		"canonical_repository.host",
		"canonical_repository.owner",
		"canonical_repository.name",
		"canonical_repository.url",
		"release.source",
		"release.must_be_immutable",
		"release.allow_draft",
		"release.allow_prerelease",
		"release.require_sha256sums",
		"release.require_asset_digest",
		"release.require_artifact_attestation",
		"windows.require_authenticode",
		"windows.require_timestamp",
		"windows.publisher_subject",
		"platforms.0.os",
		"platforms.0.arch",
		"platforms.0.asset",
		"installation.scope",
		"installation.allow_elevation",
		"installation.allow_silent_source_fallback",
		"installation.automatic_background_updates",
		"installation.listen_address",
		"installation.windows",
		"installation.linux",
		"installation.windows.program_path",
		"installation.windows.state_path",
		"installation.windows.service",
		"installation.linux.program_path",
		"installation.linux.state_path",
		"installation.linux.service",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			_, err := Parse(removeJSONField(t, data, path))
			if err == nil {
				t.Fatalf("Parse() accepted policy missing %s", formatTestJSONPath(path))
			}
			if want := formatTestJSONPath(path); !strings.Contains(err.Error(), want) {
				t.Fatalf("Parse() error %q does not identify %s", err, want)
			}
		})
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	data := readRepositoryPolicyJSON(t)
	tests := []struct {
		name       string
		objectPath string
	}{
		{name: "root"},
		{name: "nested object", objectPath: "release"},
		{name: "array object", objectPath: "platforms.0"},
		{name: "nested platform install", objectPath: "installation.windows"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const field = "unexpected"
			_, err := Parse(addJSONField(t, data, test.objectPath, field))
			path := field
			if test.objectPath != "" {
				path = test.objectPath + "." + field
			}
			want := formatTestJSONPath(path)
			if err == nil {
				t.Fatalf("Parse() accepted unknown field %s", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Parse() error %q does not identify %s", err, want)
			}
		})
	}
}

func TestParseRejectsDuplicateFields(t *testing.T) {
	data := strings.ReplaceAll(string(readRepositoryPolicyJSON(t)), "\r\n", "\n")
	tests := []struct {
		name string
		path string
		line string
	}{
		{name: "root", path: "schema_version", line: "  \"schema_version\": \"1\",\n"},
		{name: "nested object", path: "release.allow_draft", line: "    \"allow_draft\": false,\n"},
		{name: "array object", path: "platforms.0.os", line: "      \"os\": \"windows\",\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(data, test.line) {
				t.Fatalf("test setup cannot find %q", test.line)
			}
			duplicate := strings.Replace(data, test.line, test.line+test.line, 1)
			_, err := Parse([]byte(duplicate))
			want := formatTestJSONPath(test.path)
			if err == nil {
				t.Fatalf("Parse() accepted duplicate field %s", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Parse() error %q does not identify %s", err, want)
			}
		})
	}
}

func TestParseRejectsTrailingJSON(t *testing.T) {
	data := append(append([]byte(nil), readRepositoryPolicyJSON(t)...), []byte("\n{}")...)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("Parse() accepted a trailing JSON value")
	}
	if !strings.Contains(err.Error(), "$") {
		t.Fatalf("Parse() error %q does not identify the document root", err)
	}
}

func TestParseRequiresExplicitSecurityLiterals(t *testing.T) {
	data := readRepositoryPolicyJSON(t)
	for _, path := range []string{
		"binary_release_enabled",
		"release.allow_draft",
		"release.allow_prerelease",
		"installation.allow_elevation",
		"installation.allow_silent_source_fallback",
		"installation.automatic_background_updates",
	} {
		t.Run(path, func(t *testing.T) {
			_, err := Parse(setJSONField(t, data, path, nil))
			want := formatTestJSONPath(path)
			if err == nil {
				t.Fatalf("Parse() accepted null instead of false at %s", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("Parse() error %q does not identify %s", err, want)
			}
		})
	}

	t.Run("windows.publisher_subject", func(t *testing.T) {
		const path = "windows.publisher_subject"
		_, err := Parse(setJSONField(t, data, path, ""))
		if err == nil {
			t.Fatal("Parse() accepted a non-null windows.publisher_subject")
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("Parse() error %q does not identify %s", err, path)
		}
	})
}

func TestValidateRejectsUnsafePolicies(t *testing.T) {
	valid := loadRepositoryPolicy(t)
	publisher := "CN=example"

	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{name: "schema version", mutate: func(p *Policy) { p.SchemaVersion = "2" }},
		{name: "canonical repository", mutate: func(p *Policy) { p.CanonicalRepository.Owner = "someone-else" }},
		{name: "invalid stable tag regex", mutate: func(p *Policy) { p.StableTagPattern = "[" }},
		{name: "different stable tag regex", mutate: func(p *Policy) { p.StableTagPattern = `^v.*$` }},
		{name: "duplicate platform", mutate: func(p *Policy) { p.Platforms = append(p.Platforms, p.Platforms[0]) }},
		{name: "unknown operating system", mutate: func(p *Policy) { p.Platforms[0].OS = "darwin" }},
		{name: "unknown architecture", mutate: func(p *Policy) { p.Platforms[0].Arch = "386" }},
		{name: "empty asset", mutate: func(p *Policy) { p.Platforms[0].Asset = "" }},
		{name: "wrong asset", mutate: func(p *Policy) { p.Platforms[0].Asset = "wrong.exe" }},
		{name: "missing platform", mutate: func(p *Policy) { p.Platforms = p.Platforms[:len(p.Platforms)-1] }},
		{name: "enabled channel without publisher", mutate: func(p *Policy) { p.BinaryReleaseEnabled = true }},
		{name: "enabled channel while compiled off", mutate: func(p *Policy) {
			p.BinaryReleaseEnabled = true
			p.Windows.PublisherSubject = &publisher
		}},
		{name: "source-only publisher", mutate: func(p *Policy) { p.Windows.PublisherSubject = &publisher }},
		{name: "release source", mutate: func(p *Policy) { p.Release.Source = "other" }},
		{name: "mutable release", mutate: func(p *Policy) { p.Release.MustBeImmutable = false }},
		{name: "draft release", mutate: func(p *Policy) { p.Release.AllowDraft = true }},
		{name: "prerelease", mutate: func(p *Policy) { p.Release.AllowPrerelease = true }},
		{name: "missing sha256 sums", mutate: func(p *Policy) { p.Release.RequireSHA256Sums = false }},
		{name: "missing asset digest", mutate: func(p *Policy) { p.Release.RequireAssetDigest = false }},
		{name: "missing attestation", mutate: func(p *Policy) { p.Release.RequireArtifactAttestation = false }},
		{name: "missing authenticode", mutate: func(p *Policy) { p.Windows.RequireAuthenticode = false }},
		{name: "missing timestamp", mutate: func(p *Policy) { p.Windows.RequireTimestamp = false }},
		{name: "installation scope", mutate: func(p *Policy) { p.Installation.Scope = "machine" }},
		{name: "elevation", mutate: func(p *Policy) { p.Installation.AllowElevation = true }},
		{name: "silent source fallback", mutate: func(p *Policy) { p.Installation.AllowSilentSourceFallback = true }},
		{name: "background updates", mutate: func(p *Policy) { p.Installation.AutomaticBackgroundUpdate = true }},
		{name: "listen address", mutate: func(p *Policy) { p.Installation.ListenAddress = "0.0.0.0" }},
		{name: "windows install path", mutate: func(p *Policy) { p.Installation.Windows.ProgramPath = "wrong" }},
		{name: "linux service", mutate: func(p *Policy) { p.Installation.Linux.Service = "system" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Platforms = append([]PlatformAsset(nil), valid.Platforms...)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() accepted unsafe policy")
			}
		})
	}
}

func loadRepositoryPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := Load(repositoryFile(t, "install-policy.json"))
	if err != nil {
		t.Fatalf("load repository policy: %v", err)
	}
	return policy
}

func readRepositoryWorkflow(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(repositoryFile(t, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	return string(data)
}

func readRepositoryPolicyJSON(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(repositoryFile(t, "install-policy.json"))
	if err != nil {
		t.Fatalf("read repository policy: %v", err)
	}
	return data
}

func removeJSONField(t *testing.T, data []byte, path string) []byte {
	t.Helper()
	document := decodeTestJSON(t, data)
	segments := strings.Split(path, ".")
	object := testJSONObjectAtPath(t, document, segments[:len(segments)-1])
	field := segments[len(segments)-1]
	if _, ok := object[field]; !ok {
		t.Fatalf("test setup cannot find %s", formatTestJSONPath(path))
	}
	delete(object, field)
	return encodeTestJSON(t, document)
}

func addJSONField(t *testing.T, data []byte, objectPath, field string) []byte {
	t.Helper()
	document := decodeTestJSON(t, data)
	var segments []string
	if objectPath != "" {
		segments = strings.Split(objectPath, ".")
	}
	object := testJSONObjectAtPath(t, document, segments)
	object[field] = true
	return encodeTestJSON(t, document)
}

func setJSONField(t *testing.T, data []byte, path string, value any) []byte {
	t.Helper()
	document := decodeTestJSON(t, data)
	segments := strings.Split(path, ".")
	object := testJSONObjectAtPath(t, document, segments[:len(segments)-1])
	field := segments[len(segments)-1]
	if _, ok := object[field]; !ok {
		t.Fatalf("test setup cannot find %s", formatTestJSONPath(path))
	}
	object[field] = value
	return encodeTestJSON(t, document)
}

func decodeTestJSON(t *testing.T, data []byte) any {
	t.Helper()
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode test policy: %v", err)
	}
	return document
}

func encodeTestJSON(t *testing.T, document any) []byte {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode test policy: %v", err)
	}
	return data
}

func testJSONObjectAtPath(t *testing.T, document any, segments []string) map[string]any {
	t.Helper()
	current := document
	for _, segment := range segments {
		if index, err := strconv.Atoi(segment); err == nil {
			array, ok := current.([]any)
			if !ok || index < 0 || index >= len(array) {
				t.Fatalf("test path segment %q is not a valid array index", segment)
			}
			current = array[index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("test path segment %q does not select an object", segment)
		}
		var exists bool
		current, exists = object[segment]
		if !exists {
			t.Fatalf("test path segment %q does not exist", segment)
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		t.Fatal("test path does not select an object")
	}
	return object
}

func formatTestJSONPath(path string) string {
	var formatted string
	for _, segment := range strings.Split(path, ".") {
		if _, err := strconv.Atoi(segment); err == nil {
			formatted += "[" + segment + "]"
		} else if formatted == "" {
			formatted = segment
		} else {
			formatted += "." + segment
		}
	}
	return formatted
}

func repositoryFile(t *testing.T, elements ...string) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get package working directory: %v", err)
	}
	root := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolve repository root from %s: %v", workingDirectory, err)
	}
	return filepath.Join(append([]string{root}, elements...)...)
}
