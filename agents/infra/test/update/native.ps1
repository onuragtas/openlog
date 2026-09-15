<#
Self-update end-to-end scenario for the Windows service (CI job "infra-agent (Windows)", D-104/D-113): builds agent versions
-From and -To trusting a fresh test key, signs windows zip releases, serves them together with a fake ingest (OTLP sink +
scripted sync, e2e serve -scenario native), installs -From with scripts/install.ps1, lets the fake backend order an upgrade
to -To and then a rollback to -From, and checks the current link and the running service. Needs an elevated shell and Go.
Changes the host: run it on a throwaway machine (CI runner).

  ./agents/infra/test/update/native.ps1 [-Work DIR] [-Port 18475] [-TimeoutSeconds 480] [-From 0.9.0] [-To 0.9.1]
#>
[CmdletBinding()]
param(
    [string]$Work = (Join-Path ([IO.Path]::GetTempPath()) 'openlog-update-e2e'),
    [int]$Port = 18475,
    [int]$TimeoutSeconds = 480,
    [string]$From = '0.9.0',
    [string]$To = '0.9.1'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Agent = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Repo = (Resolve-Path (Join-Path $Agent '..\..')).Path
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$Mod = 'github.com/onuragtas/openlog/agents/infra/internal'
$Base = "http://127.0.0.1:$Port/releases"
$ServiceName = 'openlog-infra-agent'
$programFiles = $env:ProgramW6432
if (-not $programFiles) { $programFiles = $env:ProgramFiles }
$Root = Join-Path $programFiles 'openlog\infra-agent'
$DataDir = Join-Path $env:ProgramData 'openlog\infra-agent'
$Config = Join-Path $DataDir 'config.yaml'
$ServerLog = Join-Path $Work 'server.log'
$InstallScript = Join-Path $Repo 'scripts\install.ps1'
$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$started = Get-Date

function Write-Step([string]$Message) { Write-Host "== $((Get-Date).ToUniversalTime().ToString('HH:mm:ss')) $Message" }

function Show-Diagnostics {
    Write-Host '---- server log'
    Get-Content -LiteralPath $ServerLog -Tail 40 -ErrorAction SilentlyContinue | Write-Host
    Write-Host '---- agent log'
    Get-Content -LiteralPath (Join-Path $DataDir 'logs\openlog-infra-agent.log') -Tail 80 -ErrorAction SilentlyContinue | Write-Host
    Write-Host '---- install root'
    Get-ChildItem -LiteralPath $Root, (Join-Path $Root 'versions') -Force -ErrorAction SilentlyContinue |
        Format-Table FullName, Attributes, @{ n = 'Target'; e = { @($_.Target) -join ',' } } -AutoSize | Out-String | Write-Host
    foreach ($f in @((Join-Path $Root 'apply-status.json'), (Join-Path $Root 'reconcile-status.json'), (Join-Path $DataDir 'state\update-state.json'))) {
        if (Test-Path -LiteralPath $f) { Write-Host "-- $f"; Get-Content -LiteralPath $f -Raw | Write-Host }
    }
    Write-Host '---- service'
    Get-CimInstance -ClassName Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue |
        Format-List Name, State, ExitCode, ServiceSpecificExitCode, PathName | Out-String | Write-Host
    & sc.exe qfailure $ServiceName | Write-Host
    Get-WinEvent -FilterHashtable @{ LogName = 'System'; ProviderName = 'Service Control Manager'; StartTime = $started } -ErrorAction SilentlyContinue |
        Select-Object -First 20 | Format-Table TimeCreated, Id, Message -AutoSize -Wrap | Out-String | Write-Host
}

function Fail([string]$Message) {
    Write-Host "NATIVE SCENARIO FAILED: $Message"
    Show-Diagnostics
    throw "native.ps1: $Message"
}

function Invoke-Checked([string]$File, [string[]]$Arguments) {
    & $File @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$File $($Arguments -join ' ') exited with $LASTEXITCODE" }
}

function Get-CurrentVersion {
    $item = Get-Item -LiteralPath (Join-Path $Root 'current') -Force -ErrorAction SilentlyContinue
    if ($null -eq $item -or -not @($item.Target)[0]) { return '' }
    return Split-Path -Leaf ([string]@($item.Target)[0]).TrimEnd('\', '/')
}

function Wait-Step([int]$N, $Server) {
    while (-not (Select-String -LiteralPath $ServerLog -Pattern "STEP $N PASSED" -Quiet -ErrorAction SilentlyContinue)) {
        if (Select-String -LiteralPath $ServerLog -Pattern 'SCENARIO FAILED' -Quiet -ErrorAction SilentlyContinue) { Fail 'the fake ingest reported a failure' }
        if ($Server.HasExited) { Fail "the fake ingest exited ($($Server.ExitCode))" }
        if (((Get-Date) - $started).TotalSeconds -gt $TimeoutSeconds) { Fail "timeout (${TimeoutSeconds}s) waiting for step $N" }
        Start-Sleep -Seconds 3
    }
    Select-String -LiteralPath $ServerLog -Pattern "STEP $N PASSED" | ForEach-Object { Write-Host $_.Line }
}

Remove-Item -LiteralPath $Work -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path (Join-Path $Work 'bin'), (Join-Path $Work 'releases') | Out-Null
$server = $null
Push-Location $Agent
try {
    Write-Step "test key, agents $From and $To (windows/$Arch), signed releases"
    $env:CGO_ENABLED = '0'
    Invoke-Checked 'go' @('run', './test/update/e2e', 'keygen', '-out', (Join-Path $Work 'keys'))
    $pub = (Get-Content -LiteralPath (Join-Path $Work 'keys\key.pub') -Raw).Trim()
    foreach ($v in @($From, $To)) {
        $ldflags = "-s -w -X $Mod/version.Version=$v -X $Mod/version.Commit=e2e -X $Mod/release.trustedKeys=$pub"
        Invoke-Checked 'go' @('build', '-trimpath', '-ldflags', $ldflags, '-o', (Join-Path $Work "bin\$v\openlog-infra-agent.exe"), './cmd/openlog-infra-agent')
    }
    $e2e = Join-Path $Work 'bin\e2e.exe'
    Invoke-Checked 'go' @('build', '-o', $e2e, './test/update/e2e')
    $releaseArgs = @('release', '-seed', (Join-Path $Work 'keys\key.seed'), '-os', 'windows', '-arch', $Arch, '-layout', 'v',
        '-out', (Join-Path $Work 'releases'), '-base-url', $Base)
    Invoke-Checked $e2e ($releaseArgs + @('-version', $From, '-binary', (Join-Path $Work "bin\$From\openlog-infra-agent.exe"), '-floor', '0.8.0'))
    Invoke-Checked $e2e ($releaseArgs + @('-version', $To, '-binary', (Join-Path $Work "bin\$To\openlog-infra-agent.exe"), '-floor', $From))

    Write-Step "fake ingest on 127.0.0.1:$Port"
    $server = Start-Process -FilePath $e2e -PassThru -WindowStyle Hidden -RedirectStandardOutput $ServerLog `
        -RedirectStandardError (Join-Path $Work 'server.err') -ArgumentList @('serve', '-scenario', 'native', '-os', 'windows',
            '-from', $From, '-to', $To, '-arch', $Arch, '-listen', "127.0.0.1:$Port", '-releases', "`"$(Join-Path $Work 'releases')`"")
    $ready = $false
    for ($i = 0; $i -lt 30 -and -not $ready; $i++) {
        try { Invoke-WebRequest -Uri "$Base/v$From/manifest.json" -UseBasicParsing | Out-Null; $ready = $true } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $ready) { Get-Content -LiteralPath (Join-Path $Work 'server.err') -ErrorAction SilentlyContinue | Write-Host; Fail 'the fake ingest did not start' }

    Write-Step "install.ps1 -Version $From"
    & $powershell -NoProfile -ExecutionPolicy Bypass -File $InstallScript -BaseUrl $Base -Version $From `
        -LicenseKey e2e-license -Endpoint "http://127.0.0.1:$Port" -NoStart
    if ($LASTEXITCODE -ne 0) { Fail "install.ps1 exited with $LASTEXITCODE" }
    if (-not (Test-Path -LiteralPath (Join-Path $Root "versions\$From\manifest.json"))) { Fail "install.ps1 did not store the manifest of $From" }
    # Short collection interval: the update is confirmed by the first successful export.
    Add-Content -LiteralPath $Config -Value "`ninterval: 10s"
    Start-Service -Name $ServiceName

    Wait-Step 1 $server
    Wait-Step 2 $server
    if ((Get-CurrentVersion) -ne $To) { Fail "current is $(Get-CurrentVersion), not $To after the upgrade" }
    if (-not (Test-Path -LiteralPath (Join-Path $Root "versions\$To\manifest.json"))) { Fail "the self-update did not store the manifest of $To" }
    Wait-Step 3 $server
    if (-not (Select-String -LiteralPath $ServerLog -Pattern 'SCENARIO PASSED' -Quiet)) { Fail 'the fake ingest did not finish' }
    if ((Get-CurrentVersion) -ne $From) { Fail "current is $(Get-CurrentVersion), not $From after the rollback" }
    $svc = Get-Service -Name $ServiceName
    if ($svc.Status -ne 'Running') { Fail "service is $($svc.Status) after the rollback" }
    $v = & (Join-Path $Root 'current\openlog-infra-agent.exe') -version
    if (-not ("$v" -match [regex]::Escape($From))) { Fail "-version reports $v" }
    Write-Step "result: PASSED after $([int]((Get-Date) - $started).TotalSeconds)s"
    Show-Diagnostics
} finally {
    Pop-Location
    & $powershell -NoProfile -ExecutionPolicy Bypass -File $InstallScript -Uninstall -Purge 2>&1 | Out-Null
    if ($null -ne $server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
}
