param(
    [string]$Version = "0.1.0"
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$Dist = Join-Path $ProjectRoot "dist"
$Go = if ($env:GO) { $env:GO } else { "go" }
$BuildDate = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$Commit = "source"
if (Test-Path -LiteralPath (Join-Path $ProjectRoot ".git")) {
    $GitCommit = git -C $ProjectRoot rev-parse --short HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $GitCommit) {
        $Commit = ([string]$GitCommit).Trim()
    }
}

New-Item -ItemType Directory -Force -Path $Dist | Out-Null

Push-Location $ProjectRoot
try {
    & $Go test ./...
    if ($LASTEXITCODE -ne 0) { throw "go test failed" }

    $Targets = @(
        @{ OS = "windows"; Arch = "amd64"; Suffix = ".exe" },
        @{ OS = "windows"; Arch = "arm64"; Suffix = ".exe" },
        @{ OS = "linux"; Arch = "amd64"; Suffix = "" },
        @{ OS = "linux"; Arch = "arm64"; Suffix = "" }
    )
    foreach ($Target in $Targets) {
        $env:CGO_ENABLED = "0"
        $env:GOOS = $Target.OS
        $env:GOARCH = $Target.Arch
        $Name = "codex-usage-$($Target.OS)-$($Target.Arch)$($Target.Suffix)"
        $Output = Join-Path $Dist $Name
        $Ldflags = "-s -w -X github.com/zJay26/codex-usage/internal/app.Version=$Version -X github.com/zJay26/codex-usage/internal/app.Commit=$Commit -X github.com/zJay26/codex-usage/internal/app.BuildDate=$BuildDate"
        & $Go build -trimpath -buildvcs=false -ldflags $Ldflags -o $Output ./cmd/codex-usage
        if ($LASTEXITCODE -ne 0) { throw "build failed for $($Target.OS)/$($Target.Arch)" }
    }
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

$ChecksumLines = Get-ChildItem -LiteralPath $Dist -File |
    Where-Object { $_.Name -like "codex-usage-*" } |
    Sort-Object Name |
    ForEach-Object {
        $Hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        "$Hash  $($_.Name)"
    }
$Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllLines((Join-Path $Dist "SHA256SUMS"), [string[]]$ChecksumLines, $Utf8NoBom)
Write-Host "Built artifacts in $Dist"
