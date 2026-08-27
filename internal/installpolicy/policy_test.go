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

var unsafeInstallationPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)releases/latest/download`),
	regexp.MustCompile(`(?i)(?:zjay26|edisoncccc)/codex-usage/releases/(?:download|latest)`),
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(?:curl|wget)\b[^\r\n|]*\|\s*(?:sh|bash|zsh)\b`),
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])(?:irm|invoke-restmethod|iwr|invoke-webrequest)\b[^\r\n|]*\|\s*(?:iex|invoke-expression)\b`),
}

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

func TestInstallationDocumentsMatchPolicyState(t *testing.T) {
	policy := loadRepositoryPolicy(t)
	if policy.BinaryReleaseEnabled || BinaryReleaseEnabled {
		t.Fatal("installation document contract requires the binary release channel to remain disabled")
	}

	readmeLinks := map[string][]string{
		"README.md": {
			"[让 AI 安装](INSTALL.md)",
			"[手动安装](INSTALL.md)",
			"[`install-policy.json`](install-policy.json)",
		},
		"README.en.md": {
			"[Install with AI](INSTALL.en.md)",
			"[Manual installation](INSTALL.en.md)",
			"[`install-policy.json`](install-policy.json)",
		},
	}
	for name, requiredLinks := range readmeLinks {
		document := readRepositoryDocument(t, name)
		for _, link := range requiredLinks {
			if !strings.Contains(document, link) {
				t.Errorf("%s does not expose %q", name, link)
			}
		}
	}

	for _, name := range []string{
		"README.md",
		"README.en.md",
		"INSTALL.md",
		"INSTALL.en.md",
		"CODE_SIGNING.md",
		"CODE_SIGNING.en.md",
		"CONTRIBUTING.md",
		"SECURITY.md",
	} {
		document := strings.ToLower(readRepositoryDocument(t, name))
		if !strings.Contains(document, "source-only") {
			t.Errorf("%s does not state the source-only policy", name)
		}
	}

	defaultPaths := []string{
		policy.Installation.Windows.ProgramPath,
		policy.Installation.Windows.StatePath,
		policy.Installation.Linux.ProgramPath,
		policy.Installation.Linux.StatePath,
	}
	for _, name := range []string{"README.md", "README.en.md", "INSTALL.md", "INSTALL.en.md"} {
		document := readRepositoryDocument(t, name)
		for _, path := range defaultPaths {
			if !strings.Contains(document, path) {
				t.Errorf("%s does not document policy path %q", name, path)
			}
		}
	}

	overrideContracts := map[string][]string{
		"README.md": {
			"`CODEX_USAGE_HOME=<ABS>`", "状态根是 `<ABS>`", "`<ABS>/bin/codex-usage.exe`",
			"`<ABS>/bin/codex-usage`", "`<ABS>/config.json`", "`<ABS>/usage.sqlite`",
			"`<ABS>/install.json`", "`<ABS>/backups`", "`result.install_path`",
		},
		"INSTALL.md": {
			"`CODEX_USAGE_HOME=<ABS>`", "状态根是 `<ABS>`", "`<ABS>/bin/codex-usage.exe`",
			"`<ABS>/bin/codex-usage`", "`<ABS>/config.json`", "`<ABS>/usage.sqlite`",
			"`<ABS>/install.json`", "`result.install_path`",
		},
		"README.en.md": {
			"`CODEX_USAGE_HOME=<ABS>`", "state root is `<ABS>`", "`<ABS>/bin/codex-usage.exe`",
			"`<ABS>/bin/codex-usage`", "`<ABS>/config.json`", "`<ABS>/usage.sqlite`",
			"`<ABS>/install.json`", "`<ABS>/backups`", "`result.install_path`",
		},
		"INSTALL.en.md": {
			"`CODEX_USAGE_HOME=<ABS>`", "state root is `<ABS>`", "`<ABS>/bin/codex-usage.exe`",
			"`<ABS>/bin/codex-usage`", "`<ABS>/config.json`", "`<ABS>/usage.sqlite`",
			"`<ABS>/install.json`", "`result.install_path`",
		},
	}
	for name, fragments := range overrideContracts {
		document := readRepositoryDocument(t, name)
		for _, fragment := range fragments {
			if !strings.Contains(document, fragment) {
				t.Errorf("%s does not document override contract %q", name, fragment)
			}
		}
	}
}

func TestInstallationDocumentsRequireGitForSourceAcquisition(t *testing.T) {
	for _, test := range []struct {
		name      string
		required  string
		forbidden string
	}{
		{name: "INSTALL.md", required: "获取源码必须使用 Git；Git 不可用时停止", forbidden: "源码归档"},
		{name: "INSTALL.en.md", required: "Git is required to obtain source; stop if Git is unavailable", forbidden: "source archive"},
	} {
		document := readRepositoryDocument(t, test.name)
		if !strings.Contains(document, test.required) {
			t.Errorf("%s does not require the verified Git source path %q", test.name, test.required)
		}
		if strings.Contains(strings.ToLower(document), strings.ToLower(test.forbidden)) {
			t.Errorf("%s still offers the unverified alternative %q", test.name, test.forbidden)
		}
	}
}

func TestInstallationCommandBlocksFailFastAndVerifySource(t *testing.T) {
	powerShellSourceOrder := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\$Repository\s*=\s*["']https://github\.com/edisoncccc/codex-usage["']\s*$`),
		regexp.MustCompile(`(?m)^\$SourceDir\s*=`),
		regexp.MustCompile(`(?m)^if\s*\(\s*Test-Path\b[^\r\n]*\$SourceDir[^\r\n]*\)`),
		regexp.MustCompile(`(?m)^\s*throw\b`),
		regexp.MustCompile(`(?m)^git\s+clone\b[^\r\n]*\$Repository[^\r\n]*\$SourceDir\s*$`),
		regexp.MustCompile(`(?m)^Set-Location\b[^\r\n]*\$SourceDir`),
		regexp.MustCompile(`(?m)^\$Origin\s*=\s*git\s+remote\s+get-url\s+origin\s*$`),
		regexp.MustCompile(`(?m)^if\s*\(\s*\$Origin\s+-c?ne\s+\$Repository\s*\)`),
		regexp.MustCompile(`(?m)^\$Commit\s*=\s*git\s+rev-parse\b[^\r\n]*\bHEAD\s*$`),
		regexp.MustCompile(`(?m)^Write-Output\b[^\r\n]*\$Commit`),
		regexp.MustCompile(`(?m)^Get-Content\b[^\r\n]*install-policy\.json\s*$`),
		regexp.MustCompile(`(?m)^go\s+version\s*$`),
		regexp.MustCompile(`(?m)^go\s+test\s+\.\/\.\.\.\s*$`),
		regexp.MustCompile(`(?m)^go\s+build\b`),
		regexp.MustCompile(`(?m)^&\s+\.\\codex-usage\.exe\s+version\s+--json\s*$`),
		regexp.MustCompile(`(?m)^&\s+\.\\codex-usage\.exe\s+install\s*$`),
		regexp.MustCompile(`(?m)^&\s+\.\\codex-usage\.exe\s+doctor\s+--json\s*$`),
	}
	bashSourceOrder := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^repository=['"]https://github\.com/edisoncccc/codex-usage['"]\s*$`),
		regexp.MustCompile(`(?m)^source_dir=`),
		regexp.MustCompile(`(?m)^if\s+\[\[\s+-e\s+"\$source_dir"\s+\]\]`),
		regexp.MustCompile(`(?m)^\s*exit\s+1\s*$`),
		regexp.MustCompile(`(?m)^git\s+clone\b[^\r\n]*"\$repository"[^\r\n]*"\$source_dir"\s*$`),
		regexp.MustCompile(`(?m)^cd\s+[^\r\n]*"\$source_dir"\s*$`),
		regexp.MustCompile(`(?m)^origin="\$\(git\s+remote\s+get-url\s+origin\)"\s*$`),
		regexp.MustCompile(`(?m)^if\s+\[\[\s+"\$origin"\s+!=\s+"\$repository"\s+\]\]`),
		regexp.MustCompile(`(?m)^commit="\$\(git\s+rev-parse\b[^\r\n]*\bHEAD\)"\s*$`),
		regexp.MustCompile(`(?m)^printf\b[^\r\n]*"\$commit"`),
		regexp.MustCompile(`(?m)^cat\b[^\r\n]*install-policy\.json\s*$`),
		regexp.MustCompile(`(?m)^go\s+version\s*$`),
		regexp.MustCompile(`(?m)^go\s+test\s+\.\/\.\.\.\s*$`),
		regexp.MustCompile(`(?m)^CGO_ENABLED=0\s+go\s+build\b`),
		regexp.MustCompile(`(?m)^\.\/codex-usage\s+version\s+--json\s*$`),
		regexp.MustCompile(`(?m)^\.\/codex-usage\s+install\s*$`),
		regexp.MustCompile(`(?m)^\.\/codex-usage\s+doctor\s+--json\s*$`),
	}

	for _, name := range []string{"INSTALL.md", "INSTALL.en.md"} {
		document := readRepositoryDocument(t, name)
		for index, block := range fencedCodeBlocks(document, "powershell") {
			t.Run(fmt.Sprintf("%s PowerShell block %d", name, index+1), func(t *testing.T) {
				if !regexp.MustCompile(`^\$ErrorActionPreference\s*=\s*["']Stop["']`).MatchString(strings.TrimSpace(block)) {
					t.Error("PowerShell block does not enable terminating cmdlet errors")
				}
				assertPowerShellNativeCommandsCheckExit(t, block)
				assertCommandBlockHasNoDeletionOrElevation(t, block)
			})
		}
		for index, block := range fencedCodeBlocks(document, "bash") {
			t.Run(fmt.Sprintf("%s bash block %d", name, index+1), func(t *testing.T) {
				if !strings.HasPrefix(strings.TrimSpace(block), "set -euo pipefail\n") {
					t.Error("bash block does not begin with set -euo pipefail")
				}
				assertCommandBlockHasNoDeletionOrElevation(t, block)
			})
		}
		assertPatternsInOrder(t,
			fencedCodeBlockAfterHeading(t, document, "### 4.1 Windows PowerShell", "powershell"),
			powerShellSourceOrder,
		)
		assertPatternsInOrder(t,
			fencedCodeBlockAfterHeading(t, document, "### 4.2 Linux bash", "bash"),
			bashSourceOrder,
		)
	}
}

func TestCodeSigningDocumentsKeepPostUploadVerificationOrder(t *testing.T) {
	for _, test := range []struct {
		name     string
		heading  string
		patterns []*regexp.Regexp
	}{
		{
			name:    "CODE_SIGNING.md",
			heading: "## 5. 签名、哈希与 Attestation 顺序",
			patterns: []*regexp.Regexp{
				regexp.MustCompile(`GitHub Actions.*构建.*Windows/Linux`),
				regexp.MustCompile(`SignPath.*签署.*Windows EXE`),
				regexp.MustCompile(`重新计算 SHA256`),
				regexp.MustCompile(`生成 GitHub Artifact Attestation`),
				regexp.MustCompile(`创建 Draft Release`),
				regexp.MustCompile(`上传.*最终资产`),
				regexp.MustCompile(`Release API asset digest.*完整资产清单.*签名.*Attestation.*版本说明`),
				regexp.MustCompile(`发布审批者.*Immutable Release`),
			},
		},
		{
			name:    "CODE_SIGNING.en.md",
			heading: "## 5. Signing, digest, and Attestation order",
			patterns: []*regexp.Regexp{
				regexp.MustCompile(`GitHub Actions.*builds.*Windows/Linux`),
				regexp.MustCompile(`SignPath.*signs.*Windows executables`),
				regexp.MustCompile(`recompute SHA256`),
				regexp.MustCompile(`generate GitHub Artifact Attestations`),
				regexp.MustCompile(`create the Draft Release`),
				regexp.MustCompile(`upload.*final assets`),
				regexp.MustCompile(`Release API asset digests.*complete asset list.*signatures.*Attestations.*release notes`),
				regexp.MustCompile(`release approvers.*Immutable Release`),
			},
		},
	} {
		block := fencedCodeBlockAfterHeading(t, readRepositoryDocument(t, test.name), test.heading, "text")
		assertPatternsInOrder(t, block, test.patterns)
	}
}

func TestUnsafeInstallationPatternsRejectRemoteExecutionPipelines(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
	}{
		{name: "curl short flags to sh", command: "curl -fsSL https://example.invalid/install.sh | sh"},
		{name: "curl long flags mixed case to bash", command: "CURL --fail --silent --show-error --location https://example.invalid/install.sh\t|  BASH"},
		{name: "wget short flags to zsh", command: "wget -qO- https://example.invalid/install.sh|zsh"},
		{name: "wget long flags to bash", command: "WGET --quiet --output-document=- https://example.invalid/install.sh | bash"},
		{name: "curl with newline whitespace to zsh", command: "curl -fsSL https://example.invalid/install.sh |\n  zsh"},
		{name: "irm to iex", command: "irm https://example.invalid/install.ps1 | iex"},
		{name: "Invoke-RestMethod to Invoke-Expression", command: "Invoke-RestMethod -Uri https://example.invalid/install.ps1\t| Invoke-Expression"},
		{name: "iwr mixed case to iex", command: "IwR -UseBasicParsing https://example.invalid/install.ps1|IEX"},
		{name: "Invoke-WebRequest to Invoke-Expression", command: "Invoke-WebRequest -Uri https://example.invalid/install.ps1 |  Invoke-Expression"},
		{name: "Invoke-WebRequest with newline whitespace", command: "Invoke-WebRequest -Uri https://example.invalid/install.ps1 |\r\n  Invoke-Expression"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, pattern := range unsafeInstallationPatterns {
				if pattern.MatchString(test.command) {
					return
				}
			}
			t.Fatalf("unsafe command was not rejected: %q", test.command)
		})
	}
}

func TestInstallationDocumentsDoNotOfferUnsafePipesOrBinaryDownloads(t *testing.T) {
	for _, name := range []string{
		"README.md", "README.en.md", "INSTALL.md", "INSTALL.en.md",
		"CODE_SIGNING.md", "CODE_SIGNING.en.md",
	} {
		document := readRepositoryDocument(t, name)
		for _, pattern := range unsafeInstallationPatterns {
			if match := pattern.FindString(document); match != "" {
				t.Errorf("%s contains unsafe installation pattern %q", name, match)
			}
		}
	}
}

func TestBilingualInstallationDocumentsHaveMatchingHeadings(t *testing.T) {
	for _, pair := range []struct {
		name        string
		chinese     string
		english     string
		chineseLink string
		englishLink string
		headings    []bilingualHeading
	}{
		{
			name:        "installation guide",
			chinese:     "INSTALL.md",
			english:     "INSTALL.en.md",
			chineseLink: "[English](INSTALL.en.md)",
			englishLink: "[简体中文](INSTALL.md)",
			headings: []bilingualHeading{
				{level: "#", chinese: "Codex Usage Dashboard 安装指南", english: "Codex Usage Dashboard Installation Guide"},
				{level: "##", chinese: "1. 当前状态", english: "1. Current status"},
				{level: "##", chinese: "2. 支持范围与前置条件", english: "2. Supported platforms and prerequisites"},
				{level: "##", chinese: "3. 让 AI 安装", english: "3. Install with AI"},
				{level: "###", chinese: "3.1 唯一可信入口", english: "3.1 The only trusted entry point"},
				{level: "###", chinese: "3.2 AI 执行协议", english: "3.2 AI execution protocol"},
				{level: "###", chinese: "3.3 一次确认清单", english: "3.3 One-confirmation checklist"},
				{level: "##", chinese: "4. 人工安装", english: "4. Manual installation"},
				{level: "###", chinese: "4.1 Windows PowerShell", english: "4.1 Windows PowerShell"},
				{level: "###", chinese: "4.2 Linux bash", english: "4.2 Linux bash"},
				{level: "##", chinese: "5. 从源码构建", english: "5. Build from source"},
				{level: "###", chinese: "5.1 Windows 构建", english: "5.1 Windows build"},
				{level: "###", chinese: "5.2 Linux 构建", english: "5.2 Linux build"},
				{level: "##", chinese: "6. 安装、进度与机器回执", english: "6. Installation, progress, and machine receipt"},
				{level: "###", chinese: "6.1 人工交互", english: "6.1 Human interaction"},
				{level: "###", chinese: "6.2 AI 或自动化调用", english: "6.2 AI or automation"},
				{level: "###", chinese: "6.3 进度和终态", english: "6.3 Progress and terminal event"},
				{level: "##", chinese: "7. 健康检查", english: "7. Health check"},
				{level: "##", chinese: "8. 更新", english: "8. Updates"},
				{level: "##", chinese: "9. 卸载与数据保留", english: "9. Uninstall and data retention"},
				{level: "###", chinese: "9.1 默认保留数据", english: "9.1 Keep data by default"},
				{level: "###", chinese: "9.2 明确清除数据", english: "9.2 Explicitly purge data"},
				{level: "##", chinese: "10. 默认路径与 CODEX_USAGE_HOME 覆盖", english: "10. Default paths and CODEX_USAGE_HOME override"},
				{level: "##", chinese: "11. 网络与隐私边界", english: "11. Network and privacy boundaries"},
				{level: "##", chinese: "12. Windows Smart App Control", english: "12. Windows Smart App Control"},
				{level: "##", chinese: "13. 失败处理与后续动作", english: "13. Failure handling and next action"},
			},
		},
		{
			name:        "code signing policy",
			chinese:     "CODE_SIGNING.md",
			english:     "CODE_SIGNING.en.md",
			chineseLink: "[English](CODE_SIGNING.en.md)",
			englishLink: "[简体中文](CODE_SIGNING.md)",
			headings: []bilingualHeading{
				{level: "#", chinese: "Codex Usage Dashboard 代码签名政策", english: "Codex Usage Dashboard Code-Signing Policy"},
				{level: "##", chinese: "1. 当前状态", english: "1. Current status"},
				{level: "##", chinese: "2. 适用范围与目标", english: "2. Scope and objective"},
				{level: "##", chinese: "3. 信任与角色", english: "3. Trust and roles"},
				{level: "###", chinese: "3.1 GitHub Actions", english: "3.1 GitHub Actions"},
				{level: "###", chinese: "3.2 SignPath Foundation", english: "3.2 SignPath Foundation"},
				{level: "###", chinese: "3.3 维护者", english: "3.3 Maintainers"},
				{level: "###", chinese: "3.4 发布审批者", english: "3.4 Release approvers"},
				{level: "##", chinese: "4. 未来发布门禁", english: "4. Future release gates"},
				{level: "##", chinese: "5. 签名、哈希与 Attestation 顺序", english: "5. Signing, digest, and Attestation order"},
				{level: "##", chinese: "6. 失败处理", english: "6. Failure handling"},
				{level: "##", chinese: "7. 变更控制与安全报告", english: "7. Change control and security reporting"},
			},
		},
	} {
		t.Run(pair.name, func(t *testing.T) {
			chinese := readRepositoryDocument(t, pair.chinese)
			english := readRepositoryDocument(t, pair.english)
			if !strings.Contains(documentHeader(chinese), pair.chineseLink) {
				t.Errorf("%s does not link to %s at the top", pair.chinese, pair.english)
			}
			if !strings.Contains(documentHeader(english), pair.englishLink) {
				t.Errorf("%s does not link to %s at the top", pair.english, pair.chinese)
			}
			chineseHeadings := markdownHeadings(chinese)
			englishHeadings := markdownHeadings(english)
			if len(chineseHeadings) != len(pair.headings) || len(englishHeadings) != len(pair.headings) {
				t.Fatalf("heading count differs: %s=%d %s=%d expected=%d", pair.chinese, len(chineseHeadings), pair.english, len(englishHeadings), len(pair.headings))
			}
			for index, expected := range pair.headings {
				if chineseHeadings[index].level != expected.level || chineseHeadings[index].text != expected.chinese {
					t.Errorf("%s heading %d = %#v, want %s %q", pair.chinese, index+1, chineseHeadings[index], expected.level, expected.chinese)
				}
				if englishHeadings[index].level != expected.level || englishHeadings[index].text != expected.english {
					t.Errorf("%s heading %d = %#v, want %s %q", pair.english, index+1, englishHeadings[index], expected.level, expected.english)
				}
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

func readRepositoryDocument(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(repositoryFile(t, name))
	if err != nil {
		t.Fatalf("read repository document %s: %v", name, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func documentHeader(document string) string {
	if len(document) > 512 {
		return document[:512]
	}
	return document
}

type bilingualHeading struct {
	level   string
	chinese string
	english string
}

type markdownHeading struct {
	level string
	text  string
}

func markdownHeadings(document string) []markdownHeading {
	heading := regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	var headings []markdownHeading
	for _, line := range strings.Split(document, "\n") {
		if match := heading.FindStringSubmatch(line); match != nil {
			headings = append(headings, markdownHeading{level: match[1], text: match[2]})
		}
	}
	return headings
}

func fencedCodeBlockAfterHeading(t *testing.T, document, heading, language string) string {
	t.Helper()
	headingMarker := heading + "\n"
	headingIndex := strings.Index(document, headingMarker)
	if headingIndex < 0 {
		t.Fatalf("document does not contain heading %q", heading)
	}
	afterHeading := document[headingIndex+len(headingMarker):]
	fence := "```" + language + "\n"
	fenceIndex := strings.Index(afterHeading, fence)
	if fenceIndex < 0 {
		t.Fatalf("section %q does not contain a %s code block", heading, language)
	}
	blockStart := fenceIndex + len(fence)
	blockEnd := strings.Index(afterHeading[blockStart:], "\n```")
	if blockEnd < 0 {
		t.Fatalf("section %q has an unterminated %s code block", heading, language)
	}
	return afterHeading[blockStart : blockStart+blockEnd]
}

func fencedCodeBlocks(document, language string) []string {
	pattern := regexp.MustCompile("(?s)```" + regexp.QuoteMeta(language) + "\\n(.*?)\\n```")
	matches := pattern.FindAllStringSubmatch(document, -1)
	blocks := make([]string, 0, len(matches))
	for _, match := range matches {
		blocks = append(blocks, match[1])
	}
	return blocks
}

func assertPatternsInOrder(t *testing.T, text string, patterns []*regexp.Regexp) {
	t.Helper()
	cursor := 0
	for _, pattern := range patterns {
		match := pattern.FindStringIndex(text[cursor:])
		if match == nil {
			t.Errorf("missing or out-of-order pattern %q", pattern)
			return
		}
		cursor += match[1]
	}
}

func assertPowerShellNativeCommandsCheckExit(t *testing.T, block string) {
	t.Helper()
	nativeCommand := regexp.MustCompile(`^(?:git|go)\s|^\$[A-Za-z][A-Za-z0-9_]*\s*=\s*git\s|^&\s+\.\\codex-usage\.exe\s`)
	lastExitCheck := regexp.MustCompile(`^if\s*\(\s*\$LASTEXITCODE\s+-ne\s+0\s*\)`)
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !nativeCommand.MatchString(trimmed) {
			continue
		}
		next := index + 1
		for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next >= len(lines) || !lastExitCheck.MatchString(strings.TrimSpace(lines[next])) {
			t.Errorf("PowerShell native command does not immediately check $LASTEXITCODE: %q", trimmed)
		}
	}
}

func assertCommandBlockHasNoDeletionOrElevation(t *testing.T, block string) {
	t.Helper()
	lower := strings.ToLower(block)
	for _, forbidden := range []string{"remove-item", "rm -", "sudo", "runas", "-verb runas"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("command block contains forbidden deletion or elevation fragment %q", forbidden)
		}
	}
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
