<#
CI smoke test for the Windows packaging (windows-latest, elevated). Installs and uninstalls the MSI, then (with
-ReleaseDir) scripts/install.ps1 against a local HTTP server.

  ./test-install.ps1 -Msi dist\openlog-infra-agent_0.5.0_windows_amd64.msi -Version 0.5.0 [-ReleaseDir dist\release]

-ReleaseDir: a directory with manifest.json, manifest.json.sig and openlog-infra-agent_<v>_windows_<arch>.zip; it is
served as http://127.0.0.1:<port>/v<version>/. -Zip is copied into it when it is not there yet. When the release
binary cannot verify the manifest signature (test-signed releases), pass -SkipRerun to skip the second install.ps1
run (which verifies with -verify-release).
#>
[CmdletBinding()]
param(
    [string]$Msi,
    [string]$Zip,
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$ReleaseDir,
    [string]$InstallScript = (Join-Path $PSScriptRoot '..\..\..\..\scripts\install.ps1'),
    [int]$Port = 18473,
    [switch]$SkipRerun
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Version = $Version -replace '^v', ''
$ServiceName = 'openlog-infra-agent'
$programFiles = $env:ProgramW6432
if (-not $programFiles) { $programFiles = $env:ProgramFiles }
$Root = Join-Path $programFiles 'openlog\infra-agent'
$Current = Join-Path $Root 'current'
$Exe = Join-Path $Current 'openlog-infra-agent.exe'
$DataDir = Join-Path $env:ProgramData 'openlog\infra-agent'
$Config = Join-Path $DataDir 'config.yaml'
$ExpectedImagePath = "`"$Current\openlog-infra-agent.exe`" -config `"$Config`""
$LicenseKey = 'ci-test-key-7f3a9b2e'
$EndpointUrl = 'http://127.0.0.1:4318'

function Write-Step([string]$Message) { Write-Host "test-install: $Message" }
function Fail([string]$Message) { throw "test-install: FAIL: $Message" }

function Invoke-Native([string]$File, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $File @Arguments 2>&1 | ForEach-Object { $_.ToString() }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ ExitCode = $code; Output = (@($output) -join "`n") }
}

function Get-ServiceInfo { return Get-CimInstance -ClassName Win32_Service -Filter "Name='$ServiceName'" -ErrorAction SilentlyContinue }

function Wait-ServiceGone {
    for ($i = 0; $i -lt 30; $i++) {
        if ($null -eq (Get-ServiceInfo)) { return }
        Start-Sleep -Seconds 1
    }
    Fail "service $ServiceName is still registered"
}

function Assert-Installed([string]$Label) {
    $svc = Get-ServiceInfo
    if ($null -eq $svc) { Fail "${Label}: service $ServiceName is not registered" }
    if ($svc.PathName -ne $ExpectedImagePath) { Fail "${Label}: ImagePath is [$($svc.PathName)], want [$ExpectedImagePath]" }
    if ($svc.StartMode -ne 'Auto') { Fail "${Label}: StartMode is $($svc.StartMode), want Auto" }
    if ($svc.StartName -ne 'LocalSystem') { Fail "${Label}: service account is $($svc.StartName), want LocalSystem" }
    # Nothing listens on the endpoint, so the agent may fail to export; being registered is enough.
    Write-Step "${Label}: service registered, state $($svc.State)"

    $item = Get-Item -LiteralPath $Current -Force -ErrorAction SilentlyContinue
    if ($null -eq $item -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) { Fail "${Label}: $Current is not a link" }
    $target = ([string]@($item.Target)[0]).TrimEnd('\', '/')
    if ((Split-Path -Leaf $target) -ne $Version) { Fail "${Label}: $Current -> $target, want versions\$Version" }
    Write-Step "${Label}: $Current -> $target"

    $r = Invoke-Native $Exe @('-version')
    if ($r.ExitCode -ne 0 -or -not $r.Output.Contains($Version)) { Fail "${Label}: -version (exit $($r.ExitCode)) does not report ${Version}: $($r.Output)" }
    Write-Step "${Label}: $($r.Output.Trim())"

    if (-not (Test-Path -LiteralPath $Config -PathType Leaf)) { Fail "${Label}: $Config is missing" }
    $text = [IO.File]::ReadAllText($Config)
    if ($text -notmatch ('(?m)^license_key:[ \t]*"?' + [regex]::Escape($LicenseKey))) { Fail "${Label}: $Config has no license_key $LicenseKey" }
    if ($text -notmatch ('(?m)^endpoint:[ \t]*"?' + [regex]::Escape($EndpointUrl))) { Fail "${Label}: $Config has no endpoint $EndpointUrl" }

    # Only SYSTEM and Administrators (and the owner/TrustedInstaller) may read the configuration.
    $allowed = @('S-1-5-18', 'S-1-5-32-544', 'S-1-3-0', 'S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464')
    $rules = (Get-Acl -LiteralPath $Config).GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])
    foreach ($rule in $rules) {
        if ($rule.AccessControlType -ne 'Allow') { continue }
        $sid = $rule.IdentityReference.Value
        if ($allowed -notcontains $sid) { Fail "${Label}: $Config grants $($rule.FileSystemRights) to $sid" }
    }
    Write-Step "${Label}: $Config has the license key and a restricted ACL"
}

function Assert-ServiceRemoved([string]$Label) {
    Wait-ServiceGone
    Write-Step "${Label}: service removed"
    if (Test-Path -LiteralPath $Current) { Fail "${Label}: $Current still exists" }
}

# --- MSI ----------------------------------------------------------------------------------------
if ($Msi) {
    $Msi = (Resolve-Path -LiteralPath $Msi).Path
    $log = Join-Path (Get-Location) 'msi-install.log'
    Write-Step "msiexec /i $Msi"
    $p = Start-Process -FilePath msiexec.exe -Wait -PassThru -ArgumentList @(
        '/i', "`"$Msi`"", '/qn', '/l*v', "`"$log`"", "LICENSE_KEY=$LicenseKey", "ENDPOINT=$EndpointUrl")
    if ($p.ExitCode -ne 0) {
        Get-Content -LiteralPath $log -Tail 80 -ErrorAction SilentlyContinue | Write-Host
        Fail "msiexec /i exited with $($p.ExitCode) (log: $log)"
    }
    Assert-Installed 'msi'

    $method = Get-ItemProperty -Path 'HKLM:\SOFTWARE\openlog\infra-agent' -Name InstallMethod -ErrorAction SilentlyContinue
    if ($null -eq $method -or $method.InstallMethod -ne 'msi') { Fail 'msi: registry InstallMethod is not msi' }
    $logText = Get-Content -LiteralPath $log -Raw
    if ($logText.Contains($LicenseKey)) { Fail "msi: the license key appears in $log" }
    Write-Step 'msi: InstallMethod=msi, license key not in the log'

    $log = Join-Path (Get-Location) 'msi-uninstall.log'
    Write-Step "msiexec /x $Msi"
    $p = Start-Process -FilePath msiexec.exe -Wait -PassThru -ArgumentList @('/x', "`"$Msi`"", '/qn', '/l*v', "`"$log`"")
    if ($p.ExitCode -ne 0) {
        Get-Content -LiteralPath $log -Tail 80 -ErrorAction SilentlyContinue | Write-Host
        Fail "msiexec /x exited with $($p.ExitCode) (log: $log)"
    }
    Assert-ServiceRemoved 'msi'
    if (Test-Path -LiteralPath $Root) { Fail "msi: $Root still exists after uninstall" }
    if (Test-Path -LiteralPath 'HKLM:\SOFTWARE\openlog\infra-agent') {
        $left = Get-ItemProperty -Path 'HKLM:\SOFTWARE\openlog\infra-agent' -Name InstallMethod -ErrorAction SilentlyContinue
        if ($null -ne $left) { Fail 'msi: registry InstallMethod still present after uninstall' }
    }
    if (-not (Test-Path -LiteralPath $Config)) { Fail "msi: uninstall removed $Config (configuration must be kept)" }
    Write-Step 'msi: uninstalled, configuration kept'
}

# --- install.ps1 --------------------------------------------------------------------------------
if (-not $ReleaseDir) {
    if ($Zip) { Write-Warning 'test-install: -Zip without -ReleaseDir (manifest.json, manifest.json.sig): skipping the install.ps1 test' }
    Write-Step 'done'
    return
}

$ReleaseDir = (Resolve-Path -LiteralPath $ReleaseDir).Path
$InstallScript = (Resolve-Path -LiteralPath $InstallScript).Path
foreach ($f in @('manifest.json', 'manifest.json.sig')) {
    if (-not (Test-Path -LiteralPath (Join-Path $ReleaseDir $f) -PathType Leaf)) { Fail "$ReleaseDir has no $f" }
}
$serveRoot = Join-Path ([IO.Path]::GetTempPath()) ('openlog-release-' + [Guid]::NewGuid().ToString('N'))
$serveDir = Join-Path $serveRoot "v$Version"
New-Item -ItemType Directory -Path $serveDir -Force | Out-Null
Copy-Item -Path (Join-Path $ReleaseDir '*') -Destination $serveDir -Recurse
if ($Zip -and -not (Test-Path -LiteralPath (Join-Path $serveDir (Split-Path -Leaf $Zip)))) {
    Copy-Item -LiteralPath $Zip -Destination $serveDir
}

$python = Get-Command python -ErrorAction SilentlyContinue
if ($null -eq $python) { $python = Get-Command python3 -ErrorAction SilentlyContinue }
if ($null -eq $python) { Fail 'python is required to serve the release directory' }
$server = Start-Process -FilePath $python.Source -PassThru -WindowStyle Hidden -ArgumentList @(
    '-m', 'http.server', "$Port", '--bind', '127.0.0.1', '--directory', "`"$serveRoot`"")
try {
    $baseUrl = "http://127.0.0.1:$Port"
    $ready = $false
    for ($i = 0; $i -lt 30 -and -not $ready; $i++) {
        try {
            Invoke-WebRequest -Uri "$baseUrl/v$Version/manifest.json" -UseBasicParsing | Out-Null
            $ready = $true
        } catch { Start-Sleep -Seconds 1 }
    }
    if (-not $ready) { Fail "the local release server on $baseUrl did not start" }

    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $installArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $InstallScript,
        '-BaseUrl', $baseUrl, '-Version', $Version, '-LicenseKey', $LicenseKey, '-Endpoint', $EndpointUrl)

    Write-Step "install.ps1 -BaseUrl $baseUrl -Version $Version (Windows PowerShell 5.1)"
    & $powershell @installArgs
    if ($LASTEXITCODE -ne 0) { Fail "install.ps1 exited with $LASTEXITCODE" }
    Assert-Installed 'install.ps1'

    if (-not $SkipRerun) {
        Write-Step 're-running install.ps1 (idempotent)'
        & $powershell @installArgs
        if ($LASTEXITCODE -ne 0) { Fail "install.ps1 re-run exited with $LASTEXITCODE" }
        Assert-Installed 'install.ps1 re-run'
    }

    Write-Step 'install.ps1 -Uninstall -Purge'
    & $powershell -NoProfile -ExecutionPolicy Bypass -File $InstallScript -Uninstall -Purge
    if ($LASTEXITCODE -ne 0) { Fail "install.ps1 -Uninstall exited with $LASTEXITCODE" }
    Assert-ServiceRemoved 'install.ps1'
    if (Test-Path -LiteralPath $Root) { Fail "install.ps1: $Root still exists after -Uninstall" }
    if (Test-Path -LiteralPath $DataDir) { Fail "install.ps1: $DataDir still exists after -Uninstall -Purge" }
} finally {
    if (-not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $serveRoot -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Step 'done'
