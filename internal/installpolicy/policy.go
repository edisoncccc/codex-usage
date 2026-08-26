package installpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type Repository struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	URL   string `json:"url"`
}

type ReleaseRequirements struct {
	Source                     string `json:"source"`
	MustBeImmutable            bool   `json:"must_be_immutable"`
	AllowDraft                 bool   `json:"allow_draft"`
	AllowPrerelease            bool   `json:"allow_prerelease"`
	RequireSHA256Sums          bool   `json:"require_sha256sums"`
	RequireAssetDigest         bool   `json:"require_asset_digest"`
	RequireArtifactAttestation bool   `json:"require_artifact_attestation"`
}

type WindowsRequirements struct {
	RequireAuthenticode bool    `json:"require_authenticode"`
	RequireTimestamp    bool    `json:"require_timestamp"`
	PublisherSubject    *string `json:"publisher_subject"`
}

type PlatformAsset struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Asset string `json:"asset"`
}

type PlatformInstall struct {
	ProgramPath string `json:"program_path"`
	StatePath   string `json:"state_path"`
	Service     string `json:"service"`
}

type InstallationPolicy struct {
	Scope                     string          `json:"scope"`
	AllowElevation            bool            `json:"allow_elevation"`
	AllowSilentSourceFallback bool            `json:"allow_silent_source_fallback"`
	AutomaticBackgroundUpdate bool            `json:"automatic_background_updates"`
	ListenAddress             string          `json:"listen_address"`
	Windows                   PlatformInstall `json:"windows"`
	Linux                     PlatformInstall `json:"linux"`
}

type Policy struct {
	SchemaVersion        string              `json:"schema_version"`
	CanonicalRepository  Repository          `json:"canonical_repository"`
	StableTagPattern     string              `json:"stable_tag_pattern"`
	BinaryReleaseEnabled bool                `json:"binary_release_enabled"`
	Release              ReleaseRequirements `json:"release"`
	Windows              WindowsRequirements `json:"windows"`
	Platforms            []PlatformAsset     `json:"platforms"`
	Installation         InstallationPolicy  `json:"installation"`
}

const BinaryReleaseEnabled = false

var (
	requiredRepository = Repository{
		Host:  "github.com",
		Owner: "edisoncccc",
		Name:  "codex-usage",
		URL:   "https://github.com/edisoncccc/codex-usage",
	}
	requiredRelease = ReleaseRequirements{
		Source:                     "github_release",
		MustBeImmutable:            true,
		AllowDraft:                 false,
		AllowPrerelease:            false,
		RequireSHA256Sums:          true,
		RequireAssetDigest:         true,
		RequireArtifactAttestation: true,
	}
	requiredAssets = map[string]string{
		"windows/amd64": "codex-usage-windows-amd64.exe",
		"windows/arm64": "codex-usage-windows-arm64.exe",
		"linux/amd64":   "codex-usage-linux-amd64",
		"linux/arm64":   "codex-usage-linux-arm64",
	}
	requiredInstallation = InstallationPolicy{
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
	policyJSONFields = []string{
		"schema_version",
		"canonical_repository",
		"stable_tag_pattern",
		"binary_release_enabled",
		"release",
		"windows",
		"platforms",
		"installation",
	}
	repositoryJSONFields = []string{"host", "owner", "name", "url"}
	releaseJSONFields    = []string{
		"source",
		"must_be_immutable",
		"allow_draft",
		"allow_prerelease",
		"require_sha256sums",
		"require_asset_digest",
		"require_artifact_attestation",
	}
	windowsJSONFields      = []string{"require_authenticode", "require_timestamp", "publisher_subject"}
	platformJSONFields     = []string{"os", "arch", "asset"}
	installationJSONFields = []string{
		"scope",
		"allow_elevation",
		"allow_silent_source_fallback",
		"automatic_background_updates",
		"listen_address",
		"windows",
		"linux",
	}
	platformInstallJSONFields = []string{"program_path", "state_path", "service"}
)

const requiredStableTagPattern = `^v[0-9]+\.[0-9]+\.[0-9]+$`

func Load(path string) (Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("read install policy: %w", err)
	}
	policy, err := Parse(data)
	if err != nil {
		return Policy{}, fmt.Errorf("parse install policy %s: %w", path, err)
	}
	return policy, nil
}

func Parse(data []byte) (Policy, error) {
	if err := validateJSONDocument(data); err != nil {
		return Policy{}, err
	}
	if err := validatePolicyJSONShape(data); err != nil {
		return Policy{}, err
	}
	var policy Policy
	if err := json.Unmarshal(data, &policy); err != nil {
		return Policy{}, fmt.Errorf("$: decode policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func validateJSONDocument(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder, ""); err != nil {
		return err
	}
	token, err := decoder.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return fmt.Errorf("$: trailing JSON: %w", err)
	}
	return fmt.Errorf("$: trailing JSON value begins with %v", token)
}

func walkJSONValue(decoder *json.Decoder, path string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%s: decode JSON: %w", displayJSONPath(path), err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%s: decode object key: %w", displayJSONPath(path), err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s: object key must be a string", displayJSONPath(path))
			}
			fieldPath := joinJSONField(path, key)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("%s: duplicate field", fieldPath)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, fieldPath); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%s: close object: %w", displayJSONPath(path), err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("%s: object has invalid closing delimiter", displayJSONPath(path))
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := walkJSONValue(decoder, joinJSONIndex(path, index)); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("%s: close array: %w", displayJSONPath(path), err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("%s: array has invalid closing delimiter", displayJSONPath(path))
		}
	default:
		return fmt.Errorf("%s: unexpected delimiter %q", displayJSONPath(path), delimiter)
	}
	return nil
}

func validatePolicyJSONShape(data []byte) error {
	root, err := requireExactJSONObject(data, "", policyJSONFields)
	if err != nil {
		return err
	}
	if _, err := requireExactJSONObject(root["canonical_repository"], "canonical_repository", repositoryJSONFields); err != nil {
		return err
	}
	release, err := requireExactJSONObject(root["release"], "release", releaseJSONFields)
	if err != nil {
		return err
	}
	windows, err := requireExactJSONObject(root["windows"], "windows", windowsJSONFields)
	if err != nil {
		return err
	}
	installation, err := requireExactJSONObject(root["installation"], "installation", installationJSONFields)
	if err != nil {
		return err
	}
	if _, err := requireExactJSONObject(installation["windows"], "installation.windows", platformInstallJSONFields); err != nil {
		return err
	}
	if _, err := requireExactJSONObject(installation["linux"], "installation.linux", platformInstallJSONFields); err != nil {
		return err
	}

	var platforms []json.RawMessage
	if err := json.Unmarshal(root["platforms"], &platforms); err != nil {
		return fmt.Errorf("platforms: must be a JSON array: %w", err)
	}
	if bytes.Equal(bytes.TrimSpace(root["platforms"]), []byte("null")) {
		return fmt.Errorf("platforms: must be a JSON array")
	}
	for index, platform := range platforms {
		path := joinJSONIndex("platforms", index)
		if _, err := requireExactJSONObject(platform, path, platformJSONFields); err != nil {
			return err
		}
	}

	for _, requirement := range []struct {
		object map[string]json.RawMessage
		path   string
		field  string
		value  string
	}{
		{object: root, field: "binary_release_enabled", value: "false"},
		{object: release, path: "release", field: "must_be_immutable", value: "true"},
		{object: release, path: "release", field: "allow_draft", value: "false"},
		{object: release, path: "release", field: "allow_prerelease", value: "false"},
		{object: release, path: "release", field: "require_sha256sums", value: "true"},
		{object: release, path: "release", field: "require_asset_digest", value: "true"},
		{object: release, path: "release", field: "require_artifact_attestation", value: "true"},
		{object: windows, path: "windows", field: "require_authenticode", value: "true"},
		{object: windows, path: "windows", field: "require_timestamp", value: "true"},
		{object: windows, path: "windows", field: "publisher_subject", value: "null"},
		{object: installation, path: "installation", field: "allow_elevation", value: "false"},
		{object: installation, path: "installation", field: "allow_silent_source_fallback", value: "false"},
		{object: installation, path: "installation", field: "automatic_background_updates", value: "false"},
	} {
		if err := requireJSONLiteral(requirement.object, requirement.path, requirement.field, requirement.value); err != nil {
			return err
		}
	}
	return nil
}

func requireExactJSONObject(data []byte, path string, fields []string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%s: must be a JSON object: %w", displayJSONPath(path), err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s: must be a JSON object", displayJSONPath(path))
	}
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
		if _, ok := object[field]; !ok {
			return nil, fmt.Errorf("%s: required field is missing", joinJSONField(path, field))
		}
	}
	for field := range object {
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("%s: unknown field", joinJSONField(path, field))
		}
	}
	return object, nil
}

func requireJSONLiteral(object map[string]json.RawMessage, path, field, value string) error {
	fieldPath := joinJSONField(path, field)
	raw, ok := object[field]
	if !ok {
		return fmt.Errorf("%s: required field is missing", fieldPath)
	}
	if string(bytes.TrimSpace(raw)) != value {
		return fmt.Errorf("%s: must be the JSON literal %s", fieldPath, value)
	}
	return nil
}

func joinJSONField(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

func joinJSONIndex(path string, index int) string {
	if path == "" {
		return fmt.Sprintf("$[%d]", index)
	}
	return fmt.Sprintf("%s[%d]", path, index)
}

func displayJSONPath(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != "1" {
		return fmt.Errorf("unsupported schema_version %q", policy.SchemaVersion)
	}
	if policy.CanonicalRepository != requiredRepository {
		return fmt.Errorf("canonical_repository must be %#v", requiredRepository)
	}
	if _, err := regexp.Compile(policy.StableTagPattern); err != nil {
		return fmt.Errorf("invalid stable_tag_pattern: %w", err)
	}
	if policy.StableTagPattern != requiredStableTagPattern {
		return fmt.Errorf("stable_tag_pattern must be %q", requiredStableTagPattern)
	}
	if policy.Release != requiredRelease {
		return fmt.Errorf("release requirements do not satisfy the trusted source policy")
	}
	if !policy.Windows.RequireAuthenticode || !policy.Windows.RequireTimestamp {
		return fmt.Errorf("windows releases must require Authenticode and timestamps")
	}
	if policy.BinaryReleaseEnabled {
		if policy.Windows.PublisherSubject == nil || strings.TrimSpace(*policy.Windows.PublisherSubject) == "" {
			return fmt.Errorf("enabled binary release channel requires publisher_subject")
		}
	} else if policy.Windows.PublisherSubject != nil {
		return fmt.Errorf("source-only policy requires a null publisher_subject")
	}
	if policy.BinaryReleaseEnabled != BinaryReleaseEnabled {
		return fmt.Errorf("binary_release_enabled does not match the compiled release channel")
	}
	if err := validatePlatforms(policy.Platforms); err != nil {
		return err
	}
	if policy.Installation != requiredInstallation {
		return fmt.Errorf("installation settings do not satisfy the source-only policy")
	}
	return nil
}

func validatePlatforms(platforms []PlatformAsset) error {
	seen := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		if platform.OS != "windows" && platform.OS != "linux" {
			return fmt.Errorf("unknown platform OS %q", platform.OS)
		}
		if platform.Arch != "amd64" && platform.Arch != "arm64" {
			return fmt.Errorf("unknown platform architecture %q", platform.Arch)
		}
		key := platform.OS + "/" + platform.Arch
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate platform %q", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(platform.Asset) == "" {
			return fmt.Errorf("platform %q has an empty asset", key)
		}
		if platform.Asset != requiredAssets[key] {
			return fmt.Errorf("platform %q asset must be %q", key, requiredAssets[key])
		}
	}
	if len(seen) != len(requiredAssets) {
		return fmt.Errorf("platforms must map all %d supported OS/architecture pairs", len(requiredAssets))
	}
	for key := range requiredAssets {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("missing platform %q", key)
		}
	}
	return nil
}
