if (
    $env:GITHUB_ACTIONS -cne "true" -or
    $env:CI -cne "true" -or
    $env:RUNNER_ENVIRONMENT -cne "github-hosted" -or
    [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP) -or
    -not [System.IO.Path]::IsPathFullyQualified($env:RUNNER_TEMP) -or
    -not (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container) -or
    [System.IO.Path]::TrimEndingDirectorySeparator([System.IO.Path]::GetFullPath((Resolve-Path -LiteralPath $env:RUNNER_TEMP).Path)) -ne
        [System.IO.Path]::TrimEndingDirectorySeparator([System.IO.Path]::GetFullPath($env:RUNNER_TEMP))
) {
    throw "This lifecycle test is restricted to a canonical GitHub-hosted RUNNER_TEMP."
}

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RunnerTemp = [System.IO.Path]::GetFullPath($env:RUNNER_TEMP)
$RunID = [guid]::NewGuid().ToString("N")
$StateRoot = Join-Path $RunnerTemp "codex-usage-lifecycle-state-$RunID"
$CodexHome = Join-Path $RunnerTemp "codex-usage-lifecycle-codex-$RunID"
$BuildRoot = Join-Path $RunnerTemp "codex-usage-lifecycle-build-$RunID"
$TempRoot = Join-Path $RunnerTemp "codex-usage-lifecycle-temp-$RunID"
New-Item -ItemType Directory -Path @($StateRoot, $CodexHome, $BuildRoot, $TempRoot) | Out-Null

$env:CODEX_USAGE_HOME = $StateRoot
$env:CODEX_HOME = $CodexHome
$env:TEMP = $TempRoot
$env:TMP = $TempRoot

function Assert-PathUnderRunnerTemp {
    param([Parameter(Mandatory)][string]$Path)

    if (-not [System.IO.Path]::IsPathFullyQualified($Path)) {
        throw "Lifecycle receipt path is not absolute: $Path"
    }
    $FullPath = [System.IO.Path]::GetFullPath($Path)
    $Relative = [System.IO.Path]::GetRelativePath($RunnerTemp, $FullPath)
    if ($Relative -eq ".." -or $Relative.StartsWith("..$([System.IO.Path]::DirectorySeparatorChar)") -or [System.IO.Path]::IsPathRooted($Relative)) {
        throw "Lifecycle receipt path escaped RUNNER_TEMP: $FullPath"
    }
}

function Assert-ReceiptPaths {
    param([Parameter(Mandatory)]$Result)

    foreach ($Name in @("install_path", "state_path", "database_path")) {
        if (-not ($Result.PSObject.Properties.Name -contains $Name)) {
            throw "Lifecycle receipt is missing $Name"
        }
        Assert-PathUnderRunnerTemp -Path ([string]$Result.$Name)
    }
}

function Invoke-CapturedProcess {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $StartInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $StartInfo.FileName = $Executable
    $StartInfo.UseShellExecute = $false
    $StartInfo.RedirectStandardOutput = $true
    $StartInfo.RedirectStandardError = $true
    $StartInfo.CreateNoWindow = $true
    foreach ($Argument in $Arguments) {
        $StartInfo.ArgumentList.Add($Argument)
    }
    $Process = [System.Diagnostics.Process]::new()
    $Process.StartInfo = $StartInfo
    if (-not $Process.Start()) {
        throw "Failed to start $Executable"
    }
    $StdoutTask = $Process.StandardOutput.ReadToEndAsync()
    $StderrTask = $Process.StandardError.ReadToEndAsync()
    $Process.WaitForExit()
    $Capture = [pscustomobject]@{
        ExitCode = $Process.ExitCode
        Stdout = $StdoutTask.GetAwaiter().GetResult()
        Stderr = $StderrTask.GetAwaiter().GetResult()
    }
    $Process.Dispose()
    return $Capture
}

function Invoke-JsonLinesCommand {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $Capture = Invoke-CapturedProcess -Executable $Executable -Arguments $Arguments
    if ($Capture.ExitCode -ne 0) {
        throw "JSON Lines command failed ($($Capture.ExitCode)): $($Capture.Stderr)`n$($Capture.Stdout)"
    }
    if (-not [string]::IsNullOrWhiteSpace($Capture.Stderr)) {
        throw "Successful JSON Lines command wrote stderr: $($Capture.Stderr)"
    }

    $Events = [System.Collections.Generic.List[object]]::new()
    foreach ($Line in ($Capture.Stdout -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($Line)) {
            continue
        }
        try {
            $Event = $Line | ConvertFrom-Json -Depth 100
        } catch {
            throw "Non-JSON line in machine output: $Line"
        }
        foreach ($Field in @("schema_version", "event", "phase", "status", "timestamp")) {
            if (-not ($Event.PSObject.Properties.Name -contains $Field) -or [string]::IsNullOrWhiteSpace([string]$Event.$Field)) {
                throw "Machine event is missing stable field ${Field}: $Line"
            }
        }
        if ([string]$Event.schema_version -ne "1") {
            throw "Unexpected schema_version: $($Event.schema_version)"
        }
        if ([string]$Event.timestamp -notmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$') {
            throw "Machine event timestamp is not RFC3339 UTC: $($Event.timestamp)"
        }
        $Events.Add($Event)
    }
    if ($Events.Count -eq 0) {
        throw "JSON Lines command emitted no events"
    }
    $TerminalEvents = @($Events | Where-Object { $_.event -eq "result" -or $_.event -eq "error" })
    if ($TerminalEvents.Count -ne 1 -or $Events[$Events.Count - 1].event -notin @("result", "error")) {
        throw "JSON Lines command must emit one final terminal event"
    }
    $Terminal = $TerminalEvents[0]
    if ($Terminal.event -ne "result" -or $Terminal.status -ne "success" -or [string]::IsNullOrWhiteSpace([string]$Terminal.code)) {
        throw "JSON Lines command did not finish successfully: $($Capture.Stdout)"
    }
    return [pscustomobject]@{ Events = $Events.ToArray(); Terminal = $Terminal }
}

function Invoke-ObjectCommand {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $Capture = Invoke-CapturedProcess -Executable $Executable -Arguments $Arguments
    if ($Capture.ExitCode -ne 0 -or -not [string]::IsNullOrWhiteSpace($Capture.Stderr)) {
        throw "JSON object command failed ($($Capture.ExitCode)): $($Capture.Stderr)`n$($Capture.Stdout)"
    }
    return $Capture.Stdout | ConvertFrom-Json -Depth 100
}

function Assert-DoctorHealthy {
    param(
        [Parameter(Mandatory)][string]$Executable,
        [Parameter(Mandatory)][string]$ExpectedVersion
    )

    $Version = Invoke-JsonLinesCommand -Executable $Executable -Arguments @("version", "--json")
    if ($Version.Terminal.result.version -ne $ExpectedVersion -or $Version.Terminal.result.dirty -ne $false) {
        throw "Installed build identity mismatch: $($Version.Terminal.result | ConvertTo-Json -Compress)"
    }
    $Doctor = Invoke-JsonLinesCommand -Executable $Executable -Arguments @("doctor", "--json") # doctor --json
    if ($Doctor.Terminal.result.status -eq "error") {
        throw "Doctor reported an error"
    }
    $IdentityChecks = @($Doctor.Terminal.result.checks | Where-Object { $_.name -eq "service_identity" -and $_.code -eq "identity_match" })
    if ($IdentityChecks.Count -ne 1) {
        throw "Doctor did not prove the service identity"
    }
    foreach ($Path in @(
        $Doctor.Terminal.result.paths.state_dir,
        $Doctor.Terminal.result.paths.config_path,
        $Doctor.Terminal.result.paths.database_path,
        $Doctor.Terminal.result.paths.install_dir,
        $Doctor.Terminal.result.paths.executable
    )) {
        Assert-PathUnderRunnerTemp -Path ([string]$Path)
    }
    foreach ($Home in @($Doctor.Terminal.result.homes)) {
        Assert-PathUnderRunnerTemp -Path ([string]$Home)
    }
}

function Get-WindowsServicePID {
    $PidPath = Join-Path $StateRoot "codex-usage.pid"
    if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
        throw "Test service PID file is missing"
    }
    $ServicePID = 0
    if (-not [int]::TryParse((Get-Content -LiteralPath $PidPath -Raw).Trim(), [ref]$ServicePID) -or $ServicePID -le 0 -or $ServicePID -eq $PID) {
        throw "Test service PID metadata is invalid"
    }
    return $ServicePID
}

function Get-WindowsServiceProcess {
    param([Parameter(Mandatory)][string]$ExpectedExecutable)

    $CanonicalExecutable = [System.IO.Path]::GetFullPath($ExpectedExecutable)
    if (-not [System.StringComparer]::OrdinalIgnoreCase.Equals($CanonicalExecutable, $ExpectedExecutable)) {
        throw "Installed executable path is not canonical: $ExpectedExecutable"
    }
    $ExpectedExecutable = $CanonicalExecutable
    $Deadline = [DateTime]::UtcNow.AddSeconds(5)
    while ([DateTime]::UtcNow -lt $Deadline) {
        $ServicePID = Get-WindowsServicePID
        try {
            $ServiceProcess = Get-Process -Id $ServicePID -ErrorAction Stop
            $ProcessPath = [System.IO.Path]::GetFullPath($ServiceProcess.Path)
            $ServiceProcess.Refresh()
            if ($ServiceProcess.HasExited) {
                throw "Service process exited during ownership validation"
            }
        } catch {
            Start-Sleep -Milliseconds 100
            continue
        }
        if (-not [System.StringComparer]::OrdinalIgnoreCase.Equals($ProcessPath, $ExpectedExecutable)) {
            throw "PID points to an unexpected executable: $ProcessPath"
        }
        return $ServiceProcess
    }
    throw "Timed out validating the installed service process"
}

function Assert-WindowsServiceState {
    param([Parameter(Mandatory)][string]$ExpectedExecutable)

    $CanonicalExecutable = [System.IO.Path]::GetFullPath($ExpectedExecutable)
    if (-not [System.StringComparer]::OrdinalIgnoreCase.Equals($CanonicalExecutable, $ExpectedExecutable)) {
        throw "Installed executable path is not canonical: $ExpectedExecutable"
    }
    $ExpectedExecutable = $CanonicalExecutable
    Assert-PathUnderRunnerTemp -Path $ExpectedExecutable
    $LauncherPath = Join-Path $StateRoot "codex-usage-start.vbs"
    Assert-PathUnderRunnerTemp -Path $LauncherPath
    if (-not (Test-Path -LiteralPath $LauncherPath -PathType Leaf)) {
        throw "Windows current-user launcher is missing"
    }

    $ExpectedRunValue = "wscript.exe //B //Nologo `"$LauncherPath`""
    $RunValue = Get-ItemPropertyValue -LiteralPath "Registry::HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run" -Name "CodexUsage" -ErrorAction Stop
    if ($RunValue -cne $ExpectedRunValue) {
        throw "HKCU Run entry does not target the isolated launcher: $RunValue"
    }

    $VBSExecutable = $ExpectedExecutable.Replace('"', '""')
    $VBSState = $StateRoot.Replace('"', '""')
    $ExpectedLauncher = 'Set shell = CreateObject("WScript.Shell")' + "`r`n" +
        'shell.Environment("Process")("CODEX_USAGE_HOME") = "' + $VBSState + '"' + "`r`n" +
        'shell.Run Chr(34) & "' + $VBSExecutable + '" & Chr(34) & " daemon", 0, False' + "`r`n"
    $ActualLauncher = [System.IO.File]::ReadAllText($LauncherPath)
    if ($ActualLauncher -cne $ExpectedLauncher) {
        throw "Windows launcher does not exactly target the installed executable and daemon argument"
    }

    $ServicePID = Get-WindowsServicePID
    $ServiceProcess = Get-WindowsServiceProcess -ExpectedExecutable $ExpectedExecutable
    if ($ServiceProcess.Id -ne $ServicePID) {
        throw "PID file changed during service ownership validation"
    }
}

function Assert-WindowsServiceRemoved {
    param(
        [Parameter(Mandatory)][string]$ExpectedExecutable,
        [Parameter(Mandatory)][int]$ExpectedPID
    )

    $CanonicalExecutable = [System.IO.Path]::GetFullPath($ExpectedExecutable)
    if (-not [System.StringComparer]::OrdinalIgnoreCase.Equals($CanonicalExecutable, $ExpectedExecutable)) {
        throw "Installed executable path is not canonical: $ExpectedExecutable"
    }
    $ExpectedExecutable = $CanonicalExecutable
    $RunValue = Get-ItemPropertyValue -LiteralPath "Registry::HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run" -Name "CodexUsage" -ErrorAction SilentlyContinue
    if ($null -ne $RunValue) {
        throw "HKCU Run entry remained after default uninstall"
    }
    foreach ($Path in @((Join-Path $StateRoot "codex-usage-start.vbs"), (Join-Path $StateRoot "codex-usage.pid"))) {
        if (Test-Path -LiteralPath $Path) {
            throw "Windows service metadata remained after default uninstall: $Path"
        }
    }

    $Deadline = [DateTime]::UtcNow.AddSeconds(5)
    while ($true) {
        $ServiceProcess = Get-Process -Id $ExpectedPID -ErrorAction SilentlyContinue
        if ($null -eq $ServiceProcess) {
            return
        }
        try {
            $ProcessPath = [System.IO.Path]::GetFullPath($ServiceProcess.Path)
        } catch {
            $ProcessPath = ""
        }
        if ($ProcessPath -and -not [System.StringComparer]::OrdinalIgnoreCase.Equals($ProcessPath, $ExpectedExecutable)) {
            return
        }
        if ([DateTime]::UtcNow -ge $Deadline) {
            throw "Installed service process remained after default uninstall: $ExpectedPID"
        }
        Start-Sleep -Milliseconds 100
    }
}

function Stop-TestService {
    param([Parameter(Mandatory)][string]$ExpectedExecutable)

    $PidPath = Join-Path $StateRoot "codex-usage.pid"
    if (-not (Test-Path -LiteralPath $PidPath -PathType Leaf)) {
        throw "Test service PID file is missing"
    }
    $ServicePID = 0
    if (-not [int]::TryParse((Get-Content -LiteralPath $PidPath -Raw).Trim(), [ref]$ServicePID) -or $ServicePID -le 0 -or $ServicePID -eq $PID) {
        throw "Test service PID metadata is invalid"
    }
    $ServiceProcess = Get-Process -Id $ServicePID -ErrorAction Stop
    if ([System.IO.Path]::GetFullPath($ServiceProcess.Path) -ne [System.IO.Path]::GetFullPath($ExpectedExecutable)) {
        throw "PID points to an unexpected executable: $($ServiceProcess.Path)"
    }
    Stop-Process -Id $ServicePID -Force
    $Deadline = [DateTime]::UtcNow.AddSeconds(15)
    while (Get-Process -Id $ServicePID -ErrorAction SilentlyContinue) {
        if ([DateTime]::UtcNow -ge $Deadline) {
            throw "Timed out stopping test service PID $ServicePID"
        }
        Start-Sleep -Milliseconds 200
    }
}

function Wait-PathAbsent {
    param(
        [Parameter(Mandatory)][string]$Path,
        [int]$TimeoutSeconds = 30
    )

    $Deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while (Test-Path -LiteralPath $Path) {
        if ([DateTime]::UtcNow -ge $Deadline) {
            throw "Timed out waiting for removal: $Path"
        }
        Start-Sleep -Milliseconds 250
    }
}

$Go = (Get-Command go -ErrorAction Stop).Source
$Commit = (& git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $Commit -notmatch "^[0-9a-f]{40}$") {
    throw "Cannot resolve a full source commit"
}
$Module = "github.com/zJay26/codex-usage/internal/app"
$BuildDate = "2026-08-27T00:00:00Z"
$OldBinary = Join-Path $BuildRoot "codex-usage-old.exe"
$NewBinary = Join-Path $BuildRoot "codex-usage-new.exe"

$OldLdflags = "-s -w -X $Module.Version=2.3.5 -X $Module.Commit=$Commit -X $Module.BuildDirty=false -X $Module.BuildDate=$BuildDate"
& $Go build -trimpath -buildvcs=false -ldflags $OldLdflags -o $OldBinary ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "old identity build failed" }
$NewLdflags = "-s -w -X $Module.Version=2.3.6 -X $Module.Commit=$Commit -X $Module.BuildDirty=false -X $Module.BuildDate=$BuildDate"
& $Go build -trimpath -buildvcs=false -ldflags $NewLdflags -o $NewBinary ./cmd/codex-usage
if ($LASTEXITCODE -ne 0) { throw "new identity build failed" }

$SessionDir = Join-Path $CodexHome "sessions\2026\08\27"
New-Item -ItemType Directory -Path $SessionDir | Out-Null
$RolloutPath = Join-Path $SessionDir "rollout-synthetic-lifecycle.jsonl"
$SyntheticRollout = @(
    '{"timestamp":"2026-08-27T00:00:00Z","type":"session_meta","payload":{"id":"synthetic-lifecycle-session","cwd":"C:\\synthetic\\project","originator":"codex_cli_rs"}}',
    '{"timestamp":"2026-08-27T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":24},"last_token_usage":{"input_tokens":20,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":24}}}}',
    '{"timestamp":"2026-08-27T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":120,"cached_input_tokens":80,"cache_write_input_tokens":0,"output_tokens":24,"reasoning_output_tokens":5,"total_tokens":144},"last_token_usage":{"input_tokens":100,"cached_input_tokens":70,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":4,"total_tokens":120}}}}'
)
[System.IO.File]::WriteAllLines($RolloutPath, $SyntheticRollout, [System.Text.UTF8Encoding]::new($false))

$PortProbe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$PortProbe.Start()
$Port = ([System.Net.IPEndPoint]$PortProbe.LocalEndpoint).Port
$PortProbe.Stop()
$ConfigPath = Join-Path $StateRoot "config.json"
$ConfigJSON = [ordered]@{
    listen_address = "127.0.0.1"
    port = $Port
    scan_interval_seconds = 600
    extra_codex_homes = @()
    pricing_overrides = @{}
} | ConvertTo-Json -Depth 10
[System.IO.File]::WriteAllText($ConfigPath, $ConfigJSON + "`n", [System.Text.UTF8Encoding]::new($false))

Write-Host "Scenario 1: fresh old identity install"
$OldInstall = Invoke-JsonLinesCommand -Executable $OldBinary -Arguments @("install", "--yes", "--json") # install --yes --json
Assert-ReceiptPaths -Result $OldInstall.Terminal.result
if ($OldInstall.Terminal.result.identity.version -ne "2.3.5" -or $OldInstall.Terminal.result.service_mode -ne "persistent") {
    throw "Fresh install identity/service mode mismatch"
}
$InstalledBinary = [string]$OldInstall.Terminal.result.install_path
Assert-DoctorHealthy -Executable $InstalledBinary -ExpectedVersion "2.3.5"
Assert-WindowsServiceState -ExpectedExecutable $InstalledBinary
if (-not (Test-Path -LiteralPath $OldInstall.Terminal.result.database_path -PathType Leaf)) {
    throw "Fresh install did not create the isolated database"
}

Write-Host "Scenario 2: idempotent same-binary install"
$InstalledSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $InstalledBinary).Hash
$RecordPath = Join-Path $StateRoot "install.json"
$RecordSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $RecordPath).Hash
$SameInstall = Invoke-JsonLinesCommand -Executable $OldBinary -Arguments @("install", "--yes", "--json") # install --yes --json
Assert-ReceiptPaths -Result $SameInstall.Terminal.result
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $InstalledBinary).Hash -ne $InstalledSHA256 -or
    (Get-FileHash -Algorithm SHA256 -LiteralPath $RecordPath).Hash -ne $RecordSHA256) {
    throw "Idempotent install changed the executable digest or install record"
}
Assert-DoctorHealthy -Executable $InstalledBinary -ExpectedVersion "2.3.5"
Assert-WindowsServiceState -ExpectedExecutable $InstalledBinary

Write-Host "Scenario 3: stopped service repair"
Stop-TestService -ExpectedExecutable $InstalledBinary
$Repair = Invoke-JsonLinesCommand -Executable $OldBinary -Arguments @("install", "--yes", "--json") # install --yes --json
Assert-ReceiptPaths -Result $Repair.Terminal.result
Assert-DoctorHealthy -Executable $InstalledBinary -ExpectedVersion "2.3.5"
Assert-WindowsServiceState -ExpectedExecutable $InstalledBinary

$ConfigSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $ConfigPath).Hash
$DatabasePath = [string]$Repair.Terminal.result.database_path
$BeforeSummary = Invoke-ObjectCommand -Executable $InstalledBinary -Arguments @("summary", "--since", "all", "--json")
if ([int64]$BeforeSummary.event_count -lt 1) {
    throw "Synthetic token fixture did not create an event before upgrade"
}

Write-Host "Scenario 4: upgrade to the new identity"
$Upgrade = Invoke-JsonLinesCommand -Executable $NewBinary -Arguments @("install", "--yes", "--json") # install --yes --json
Assert-ReceiptPaths -Result $Upgrade.Terminal.result
if ($Upgrade.Terminal.result.identity.version -ne "2.3.6" -or
    (Get-FileHash -Algorithm SHA256 -LiteralPath $InstalledBinary).Hash -ne (Get-FileHash -Algorithm SHA256 -LiteralPath $NewBinary).Hash) {
    throw "Upgrade did not activate the new identity"
}
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $ConfigPath).Hash -ne $ConfigSHA256 -or -not (Test-Path -LiteralPath $DatabasePath -PathType Leaf)) {
    throw "Upgrade did not preserve config/database"
}
$AfterSummary = Invoke-ObjectCommand -Executable $InstalledBinary -Arguments @("summary", "--since", "all", "--json")
if ([int64]$AfterSummary.event_count -lt [int64]$BeforeSummary.event_count) {
    throw "Upgrade lost synthetic database events"
}
Assert-DoctorHealthy -Executable $InstalledBinary -ExpectedVersion "2.3.6"
Assert-WindowsServiceState -ExpectedExecutable $InstalledBinary

Write-Host "Scenario 5: JSON Lines scan"
$Scan = Invoke-JsonLinesCommand -Executable $InstalledBinary -Arguments @("scan", "--json") # scan --json
$ScanProgress = @($Scan.Events | Where-Object { $_.event -eq "progress" -and $_.phase -eq "scan" })
if ($ScanProgress.Count -lt 1) {
    throw "Scan emitted no progress event"
}

Write-Host "Scenario 6-7: default uninstall and scheduled removal"
$DefaultServicePID = Get-WindowsServicePID
$DefaultUninstall = Invoke-JsonLinesCommand -Executable $InstalledBinary -Arguments @("uninstall", "--yes", "--json") # uninstall --yes --json
Assert-ReceiptPaths -Result $DefaultUninstall.Terminal.result
if ($DefaultUninstall.Terminal.result.program_removed -ne $false -or
    $DefaultUninstall.Terminal.result.removal_scheduled -ne $true -or
    $DefaultUninstall.Terminal.result.data_preserved -ne $true -or
    $DefaultUninstall.Terminal.result.purged -ne $false) {
    throw "Windows default uninstall receipt is inaccurate"
}
if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf) -or -not (Test-Path -LiteralPath $DatabasePath -PathType Leaf)) {
    throw "Default uninstall did not preserve config/database"
}
Assert-WindowsServiceRemoved -ExpectedExecutable $InstalledBinary -ExpectedPID $DefaultServicePID
Wait-PathAbsent -Path $InstalledBinary

Write-Host "Scenario 8: reinstall then purge"
$Reinstall = Invoke-JsonLinesCommand -Executable $NewBinary -Arguments @("install", "--yes", "--json") # install --yes --json
Assert-ReceiptPaths -Result $Reinstall.Terminal.result
$InstalledBinary = [string]$Reinstall.Terminal.result.install_path
Assert-DoctorHealthy -Executable $InstalledBinary -ExpectedVersion "2.3.6"
$Purge = Invoke-JsonLinesCommand -Executable $InstalledBinary -Arguments @("uninstall", "--purge", "--yes", "--json") # uninstall --purge --yes --json
Assert-ReceiptPaths -Result $Purge.Terminal.result
if ($Purge.Terminal.result.program_removed -ne $false -or
    $Purge.Terminal.result.removal_scheduled -ne $true -or
    $Purge.Terminal.result.data_preserved -ne $false -or
    $Purge.Terminal.result.purged -ne $false) {
    throw "Windows purge receipt is inaccurate"
}

Write-Host "Scenario 9: wait for scheduled executable/state removal"
Wait-PathAbsent -Path $InstalledBinary
Wait-PathAbsent -Path $StateRoot
$RunValue = Get-ItemPropertyValue -LiteralPath "Registry::HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run" -Name "CodexUsage" -ErrorAction SilentlyContinue
if ($null -ne $RunValue) {
    throw "HKCU Run entry remained after purge"
}

Write-Host "Windows current-user lifecycle completed with stable schema_version/event/phase/status/timestamp and one terminal event per command."
