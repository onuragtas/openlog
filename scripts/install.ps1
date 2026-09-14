<#
openlog infrastructure agent installer for Windows (Windows PowerShell 5.1 or PowerShell 7, run as Administrator).

  & ([scriptblock]::Create((Invoke-RestMethod https://github.com/onuragtas/openlog/releases/latest/download/install.ps1))) -LicenseKey KEY -Endpoint https://ingest.example.com:4318

  or, after downloading install.ps1:  powershell -ExecutionPolicy Bypass -File install.ps1 -LicenseKey KEY -Endpoint URL

Options (environment variable in brackets):
  -LicenseKey KEY     ingest license key                                   [OPENLOG_LICENSE_KEY]
  -Endpoint URL       OTLP/HTTP endpoint of openlog-ingest                  [OPENLOG_ENDPOINT]
  -Version V          version to install (default: latest on the channel)   [OPENLOG_VERSION]
  -Channel C          stable (default) or beta                              [OPENLOG_CHANNEL]
  -BaseUrl URL        releases root or mirror; files are fetched from URL/v<version>/
                      and the index from URL/index.json                    [OPENLOG_RELEASE_BASE_URL]
  -IndexUrl URL       release index                                         [OPENLOG_RELEASE_INDEX_URL]
  -Arch A             amd64 or arm64 (default: this machine)
  -NoStart            install and configure, but do not (re)start the service
  -Uninstall          stop and delete the service and remove the install root (keeps the configuration)
  -Purge              with -Uninstall: also remove %ProgramData%\openlog\infra-agent (configuration and state)

Layout: %ProgramFiles%\openlog\infra-agent\versions\<v>\ (release files, manifest.json, manifest.json.sig),
current -> versions\<v> (directory symbolic link), configuration %ProgramData%\openlog\infra-agent\config.yaml,
state %ProgramData%\openlog\infra-agent\state, service openlog-infra-agent (LocalSystem, created by the agent).

Trust model: this bootstrap download relies on HTTPS. The installer fetches manifest.json from the release source
and checks the size and SHA-256 of the downloaded archive against it; when a release of the agent is already
installed, that trusted binary also verifies the Ed25519 signature of the manifest (-verify-release). Every later
update is applied by the agent itself, which verifies the signature against the public keys compiled into it
(docs/contracts/releases-updates.md).

Re-running the installer is safe: it upgrades to the requested/latest version, keeps an agent that already updated
itself to a newer version, and updates license key and endpoint when given. Every run reconciles the installation
with the running release (openlog-infra-agent -reconcile: service definition, directory ACLs).
Hosts installed with the MSI package are managed with msiexec / "Apps & features" instead.
#>
[CmdletBinding()]
param(
    [string]$LicenseKey = $env:OPENLOG_LICENSE_KEY,
    [string]$Endpoint = $env:OPENLOG_ENDPOINT,
    [string]$Version = $env:OPENLOG_VERSION,
    [string]$Channel = $env:OPENLOG_CHANNEL,
    [string]$BaseUrl = $env:OPENLOG_RELEASE_BASE_URL,
    [string]$IndexUrl = $env:OPENLOG_RELEASE_INDEX_URL,
    [string]$Arch,
    [switch]$NoStart,
    [switch]$Uninstall,
    [switch]$Purge,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$GitHubReleases = 'https://github.com/onuragtas/openlog/releases'
$ServiceName = 'openlog-infra-agent'
$ExeName = 'openlog-infra-agent.exe'
$RegistryKey = 'SOFTWARE\openlog\infra-agent'
$SemVerPattern = '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

function Write-InstallLog([string]$Message) { Write-Host "openlog-install: $Message" }
function Stop-Install([string]$Message) { throw "openlog-install: error: $Message" }

if ($Help) {
    Write-Host 'openlog infrastructure agent installer for Windows.'
    Write-Host '  -LicenseKey KEY -Endpoint URL [-Version V] [-Channel stable|beta] [-BaseUrl URL] [-IndexUrl URL]'
    Write-Host '  [-Arch amd64|arm64] [-NoStart] | -Uninstall [-Purge]'
    Write-Host 'Environment: OPENLOG_LICENSE_KEY, OPENLOG_ENDPOINT, OPENLOG_VERSION, OPENLOG_CHANNEL,'
    Write-Host '             OPENLOG_RELEASE_BASE_URL, OPENLOG_RELEASE_INDEX_URL'
    Write-Host 'See https://github.com/onuragtas/openlog/blob/master/agents/infra/README.md'
    return
}

# --- platform -----------------------------------------------------------------------------------
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { Stop-Install 'this installer supports Windows only (use install.sh on Linux)' }
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Stop-Install 'run this installer from an elevated PowerShell (Run as Administrator)'
}
if ($PSVersionTable.PSVersion.Major -lt 6) {
    # Windows PowerShell 5.1 defaults to TLS 1.0/1.1 on older systems; GitHub requires TLS 1.2.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

# 64-bit paths even from a 32-bit PowerShell.
$programFiles = $env:ProgramW6432
if (-not $programFiles) { $programFiles = $env:ProgramFiles }
$Root = Join-Path $programFiles 'openlog\infra-agent'
$DataDir = Join-Path $env:ProgramData 'openlog\infra-agent'
$Config = Join-Path $DataDir 'config.yaml'
$Current = Join-Path $Root 'current'
$CurrentExe = Join-Path $Current $ExeName

# --- helpers ------------------------------------------------------------------------------------
function Test-ReparsePoint([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    return ($null -ne $item) -and (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)
}

# Remove-Tree deletes a directory without ever following links: links (symbolic links, junctions) are
# removed as links, their targets are left alone.
function Remove-Tree([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return }
    if (Test-ReparsePoint $Path) {
        $item = Get-Item -LiteralPath $Path -Force
        if ($item -is [IO.DirectoryInfo]) { [IO.Directory]::Delete($item.FullName) } else { [IO.File]::Delete($item.FullName) }
        return
    }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item -isnot [IO.DirectoryInfo]) {
        $item.Attributes = [IO.FileAttributes]::Normal
        [IO.File]::Delete($item.FullName)
        return
    }
    foreach ($child in $item.GetFileSystemInfos()) { Remove-Tree $child.FullName }
    [IO.Directory]::Delete($item.FullName)
}

function Get-LinkTarget([string]$Path) {
    if (-not (Test-ReparsePoint $Path)) { return $null }
    $target = @((Get-Item -LiteralPath $Path -Force).Target)
    if ($target.Count -eq 0 -or -not $target[0]) { return $null }
    return ([string]$target[0]).TrimEnd('\', '/')
}

function Get-RunningVersion {
    $target = Get-LinkTarget $Current
    if (-not $target) { return '' }
    return (Split-Path -Leaf $target)
}

# Invoke-Agent runs a native command without letting Windows PowerShell 5.1 turn its stderr into terminating errors.
# Stderr lines are shown (unless -Quiet) and returned separately from stdout.
function Invoke-Agent {
    param([string]$Exe, [string[]]$Arguments, [switch]$Quiet)
    $stdout = New-Object System.Collections.Generic.List[string]
    $stderr = New-Object System.Collections.Generic.List[string]
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & $Exe @Arguments 2>&1 | ForEach-Object {
            if ($_ -is [System.Management.Automation.ErrorRecord]) {
                $line = $_.ToString()
                $stderr.Add($line)
                if (-not $Quiet) { Write-Host $line }
            } else {
                $stdout.Add([string]$_)
            }
        }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ ExitCode = $code; Stdout = $stdout.ToArray(); Stderr = $stderr.ToArray() }
}

function Test-AgentFlag([string]$Exe, [string]$Flag) {
    if (-not (Test-Path -LiteralPath $Exe -PathType Leaf)) { return $false }
    $r = Invoke-Agent -Exe $Exe -Arguments @('-help') -Quiet
    return (($r.Stdout + $r.Stderr) -join "`n").Contains($Flag)
}

function Save-Url([string]$Url, [string]$Destination) {
    for ($attempt = 1; ; $attempt++) {
        try {
            Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing
            return
        } catch {
            if ($attempt -ge 3) { Stop-Install "download failed: $Url ($($_.Exception.Message))" }
            Start-Sleep -Seconds (2 * $attempt)
        }
    }
}

# Compare-SemVer returns -1, 0 or 1 following SemVer 2.0 precedence (build metadata ignored).
function Compare-SemVerId([string]$A, [string]$B) {
    $aNum = $A -match '^[0-9]+$'
    $bNum = $B -match '^[0-9]+$'
    if ($aNum -and $bNum) {
        $x = $A.TrimStart('0'); $y = $B.TrimStart('0')
        if ($x.Length -ne $y.Length) { if ($x.Length -lt $y.Length) { return -1 } else { return 1 } }
        return [Math]::Sign([string]::CompareOrdinal($x, $y))
    }
    if ($aNum) { return -1 }
    if ($bNum) { return 1 }
    return [Math]::Sign([string]::CompareOrdinal($A, $B))
}

function Compare-SemVer([string]$A, [string]$B) {
    $A = ($A -replace '^v', '') -replace '\+.*$', ''
    $B = ($B -replace '^v', '') -replace '\+.*$', ''
    $pa = ''; $pb = ''
    $i = $A.IndexOf('-'); if ($i -ge 0) { $pa = $A.Substring($i + 1); $A = $A.Substring(0, $i) }
    $i = $B.IndexOf('-'); if ($i -ge 0) { $pb = $B.Substring($i + 1); $B = $B.Substring(0, $i) }
    $ca = $A.Split('.'); $cb = $B.Split('.')
    for ($k = 0; $k -lt 3; $k++) {
        $x = ''; $y = ''
        if ($k -lt $ca.Count) { $x = $ca[$k] }
        if ($k -lt $cb.Count) { $y = $cb[$k] }
        $c = Compare-SemVerId $x $y
        if ($c -ne 0) { return $c }
    }
    if (-not $pa -and -not $pb) { return 0 }
    if (-not $pa) { return 1 }
    if (-not $pb) { return -1 }
    $ia = $pa.Split('.'); $ib = $pb.Split('.')
    for ($k = 0; $k -lt $ia.Count -and $k -lt $ib.Count; $k++) {
        $c = Compare-SemVerId $ia[$k] $ib[$k]
        if ($c -ne 0) { return $c }
    }
    if ($ia.Count -lt $ib.Count) { return -1 }
    if ($ia.Count -gt $ib.Count) { return 1 }
    return 0
}

function Get-MsiInstallMethod {
    try {
        $hklm = [Microsoft.Win32.RegistryKey]::OpenBaseKey([Microsoft.Win32.RegistryHive]::LocalMachine, [Microsoft.Win32.RegistryView]::Registry64)
        $key = $hklm.OpenSubKey($RegistryKey)
        if ($null -eq $key) { return '' }
        try { return [string]$key.GetValue('InstallMethod', '') } finally { $key.Close() }
    } catch {
        return ''
    }
}

# Switch-Current points current at versions\<v>: create current.new, move current aside, move current.new in place,
# delete the old link. Links are deleted as links, never recursively.
function Switch-Current([string]$TargetVersion) {
    $target = Join-Path $Root "versions\$TargetVersion"
    $newLink = "$Current.new"
    $oldLink = "$Current.old"
    foreach ($p in @($newLink, $oldLink)) {
        if (Test-Path -LiteralPath $p) {
            if (-not (Test-ReparsePoint $p)) { Stop-Install "$p exists and is not a link; remove it manually" }
            [IO.Directory]::Delete($p)
        }
    }
    New-Item -ItemType SymbolicLink -Path $newLink -Value $target | Out-Null
    if (Test-Path -LiteralPath $Current) {
        [IO.Directory]::Move($Current, $oldLink)
    }
    [IO.Directory]::Move($newLink, $Current)
    if (Test-Path -LiteralPath $oldLink) { [IO.Directory]::Delete($oldLink) }
    Write-InstallLog "$Current -> versions\$TargetVersion"
}

function Test-LicenseKeyConfigured {
    if (-not (Test-Path -LiteralPath $Config -PathType Leaf)) { return $false }
    $text = [IO.File]::ReadAllText($Config)
    return $text -match '(?m)^license_key:[ \t]*"?[^"\s#]'
}

function Get-AgentService { return Get-Service -Name $ServiceName -ErrorAction SilentlyContinue }

function Wait-ServiceStopped {
    $svc = Get-AgentService
    if ($null -eq $svc) { return }
    if ($svc.Status -ne 'Stopped') {
        try { Stop-Service -Name $ServiceName -Force -ErrorAction Stop } catch { Write-InstallLog "warning: stopping $ServiceName failed: $($_.Exception.Message)" }
        try { $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30)) } catch { Write-InstallLog "warning: $ServiceName did not stop within 30s" }
    }
}

# --- uninstall ----------------------------------------------------------------------------------
if ($Uninstall) {
    if ((Get-MsiInstallMethod) -eq 'msi') {
        Stop-Install 'openlog-infra-agent was installed with the MSI package; uninstall it from "Apps & features" or with msiexec /x'
    }
    $svc = Get-AgentService
    if ($null -ne $svc) {
        $removed = $false
        if (Test-Path -LiteralPath $CurrentExe -PathType Leaf) {
            $r = Invoke-Agent -Exe $CurrentExe -Arguments @('-uninstall-service')
            $removed = ($r.ExitCode -eq 0)
            if (-not $removed) { Write-InstallLog "warning: $ExeName -uninstall-service exited with $($r.ExitCode); falling back to sc.exe" }
        }
        if (-not $removed) {
            Wait-ServiceStopped
            $null = Invoke-Agent -Exe "$env:SystemRoot\System32\sc.exe" -Arguments @('delete', $ServiceName) -Quiet
        }
        for ($i = 0; $i -lt 30 -and $null -ne (Get-AgentService); $i++) { Start-Sleep -Seconds 1 }
        if ($null -ne (Get-AgentService)) { Write-InstallLog "warning: service $ServiceName is still registered (marked for deletion?)" }
        else { Write-InstallLog "service $ServiceName removed" }
    }
    if (Test-Path -LiteralPath $Root) {
        # The service process may take a moment to exit and release its binary.
        for ($attempt = 1; ; $attempt++) {
            try { Remove-Tree $Root; break } catch {
                if ($attempt -ge 15) { Stop-Install "cannot remove ${Root}: $($_.Exception.Message)" }
                Start-Sleep -Seconds 2
            }
        }
        $parent = Split-Path -Parent $Root
        if ((Test-Path -LiteralPath $parent) -and -not (Get-ChildItem -LiteralPath $parent -Force)) { [IO.Directory]::Delete($parent) }
        Write-InstallLog "removed $Root"
    }
    if ($Purge) {
        if (Test-Path -LiteralPath $DataDir) {
            Remove-Tree $DataDir
            $parent = Split-Path -Parent $DataDir
            if ((Test-Path -LiteralPath $parent) -and -not (Get-ChildItem -LiteralPath $parent -Force)) { [IO.Directory]::Delete($parent) }
            Write-InstallLog "removed $DataDir"
        }
    } elseif (Test-Path -LiteralPath $DataDir) {
        Write-InstallLog "kept configuration and state in $DataDir (use -Purge to remove them)"
    }
    Write-InstallLog 'uninstalled'
    return
}

# --- validation ---------------------------------------------------------------------------------
if (-not $Channel) { $Channel = 'stable' }
if ($Version) { $Version = $Version -replace '^v', '' }
$explicitVersion = [bool]$Version
if ($Channel -ne 'stable' -and $Channel -ne 'beta') { Stop-Install '-Channel must be stable or beta' }
if ($Version -and $Version -notmatch $SemVerPattern) { Stop-Install "-Version $Version is not a SemVer version" }
if ($LicenseKey -and $LicenseKey -match '["\\\s]') { Stop-Install '-LicenseKey contains invalid characters' }
if ($Endpoint) {
    if ($Endpoint -notmatch '^https?://') { Stop-Install '-Endpoint must start with https:// or http://' }
    if ($Endpoint -match '["\\\s]') { Stop-Install '-Endpoint contains invalid characters' }
}
if ($BaseUrl) { $BaseUrl = $BaseUrl.TrimEnd('/') }
foreach ($u in @($BaseUrl, $IndexUrl)) {
    if (-not $u) { continue }
    if ($u -match '^https://') { continue }
    if ($u -match '^http://') { Write-InstallLog "warning: $u is not HTTPS; only use plain HTTP for local testing"; continue }
    Stop-Install "URL must start with https:// or http://: $u"
}

if (-not $Arch) {
    $machine = $env:PROCESSOR_ARCHITEW6432
    if (-not $machine) { $machine = $env:PROCESSOR_ARCHITECTURE }
    switch ($machine) {
        'AMD64' { $Arch = 'amd64' }
        'ARM64' { $Arch = 'arm64' }
        default { Stop-Install "unsupported architecture $machine (amd64 and arm64 are supported)" }
    }
}
if ($Arch -ne 'amd64' -and $Arch -ne 'arm64') { Stop-Install '-Arch must be amd64 or arm64' }

if ((Get-MsiInstallMethod) -eq 'msi') {
    Stop-Install 'openlog-infra-agent is installed with the MSI package; upgrade it with the newer MSI (msiexec /i) instead'
}
if ((Test-Path -LiteralPath $Current) -and -not (Test-ReparsePoint $Current)) {
    Stop-Install "$Current exists but is not a link; move it away and re-run"
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('openlog-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    # --- resolve the release --------------------------------------------------------------------
    $releases = $BaseUrl
    if (-not $releases) { $releases = "$GitHubReleases/download" }

    if (-not $Version) {
        if (-not $IndexUrl) {
            if ($BaseUrl) { $IndexUrl = "$BaseUrl/index.json" } else { $IndexUrl = "$GitHubReleases/latest/download/index.json" }
        }
        Write-InstallLog "resolving the latest $Channel release from $IndexUrl"
        $indexFile = Join-Path $tmp 'index.json'
        Save-Url $IndexUrl $indexFile
        try { $index = [IO.File]::ReadAllText($indexFile) | ConvertFrom-Json } catch { Stop-Install "$IndexUrl is not valid JSON" }
        if ($index.product -ne 'openlog') { Stop-Install "$IndexUrl is not an openlog release index" }
        $entries = @()
        if ($index.channels -and $index.channels.stable) { $entries += @($index.channels.stable) }
        if ($Channel -eq 'beta' -and $index.channels -and $index.channels.beta) { $entries += @($index.channels.beta) }
        $manifestUrl = ''
        foreach ($e in $entries) {
            $v = [string]$e.version
            $u = [string]$e.manifest_url
            if (-not $v -or -not $u -or $v -notmatch $SemVerPattern) { continue }
            if (-not $Version -or (Compare-SemVer $v $Version) -eq 1) {
                $Version = $v
                $manifestUrl = $u
            }
        }
        if (-not $Version) { Stop-Install "no $Channel release found in $IndexUrl" }
        if ($BaseUrl) { $manifestUrl = "$BaseUrl/v$Version/manifest.json" }
    } else {
        $manifestUrl = "$releases/v$Version/manifest.json"
    }
    Write-InstallLog "installing openlog-infra-agent $Version ($Channel, windows/$Arch)"

    # --- decide whether to install --------------------------------------------------------------
    $running = Get-RunningVersion
    $install = $true
    if ($running -and (Test-Path -LiteralPath $CurrentExe -PathType Leaf)) {
        $cmp = 0
        if ($running -match $SemVerPattern) { $cmp = Compare-SemVer $running $Version } else { $cmp = -1 }
        if ($cmp -eq 1 -and -not $explicitVersion) {
            Write-InstallLog "the agent already runs $running (newer than $Version); keeping it"
            $install = $false
        } elseif ($cmp -eq 0) {
            Write-InstallLog "openlog-infra-agent $Version is already installed"
            $install = $false
        }
    }

    if ($install) {
        $manifestFile = Join-Path $tmp 'manifest.json'
        Save-Url $manifestUrl $manifestFile
        Save-Url "$manifestUrl.sig" "$manifestFile.sig"
        try { $manifest = [IO.File]::ReadAllText($manifestFile) | ConvertFrom-Json } catch { Stop-Install "$manifestUrl is not valid JSON" }
        if ($manifest.product -ne 'openlog') { Stop-Install "$manifestUrl is not an openlog manifest" }
        if ([string]$manifest.version -ne $Version) { Stop-Install "$manifestUrl is for version $($manifest.version), not $Version" }

        $name = "openlog-infra-agent_${Version}_windows_${Arch}.zip"
        $artifact = @($manifest.artifacts | Where-Object { $_.name -eq $name }) | Select-Object -First 1
        if ($null -eq $artifact) { Stop-Install "release $Version has no artifact $name" }
        $wantSha = ([string]$artifact.sha256).ToLowerInvariant()
        if ($wantSha -notmatch '^[0-9a-f]{64}$') { Stop-Install "manifest has no valid sha256 for $name" }
        $wantSize = [int64]$artifact.size
        $url = [string]$artifact.url
        if ($BaseUrl -or -not $url) { $url = "$releases/v$Version/$name" }

        Write-InstallLog "downloading $url"
        $zip = Join-Path $tmp $name
        Save-Url $url $zip
        $gotSize = (Get-Item -LiteralPath $zip).Length
        $gotSha = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($gotSize -ne $wantSize) { Stop-Install "${name}: size $gotSize does not match the manifest ($wantSize)" }
        if ($gotSha -ne $wantSha) { Stop-Install "${name}: sha256 $gotSha does not match the manifest ($wantSha)" }
        Write-InstallLog "sha256 verified: $gotSha"

        # A release that is already installed is trusted: let it verify the manifest signature too.
        if (Test-AgentFlag $CurrentExe '-verify-release') {
            $r = Invoke-Agent -Exe $CurrentExe -Arguments @('-verify-release', $manifestFile, '-artifact', $zip)
            if ($r.ExitCode -ne 0) { Stop-Install "the installed agent rejected release $Version (-verify-release exit code $($r.ExitCode))" }
            Write-InstallLog "manifest signature verified by the installed agent $running"
        }

        $extract = Join-Path $tmp 'x'
        Expand-Archive -LiteralPath $zip -DestinationPath $extract
        $top = Join-Path $extract "openlog-infra-agent_${Version}_windows_${Arch}"
        if (-not (Test-Path -LiteralPath (Join-Path $top $ExeName) -PathType Leaf)) { Stop-Install "unexpected archive layout in $name" }

        $versions = Join-Path $Root 'versions'
        if (-not (Test-Path -LiteralPath $versions)) { New-Item -ItemType Directory -Path $versions -Force | Out-Null }
        $dest = Join-Path $versions $Version
        $staging = "$dest.new"
        Remove-Tree $staging
        Copy-Item -LiteralPath $top -Destination $staging -Recurse
        # The signed manifest of the installed version; the agent needs it for rollback_floor.
        Copy-Item -LiteralPath $manifestFile -Destination (Join-Path $staging 'manifest.json')
        Copy-Item -LiteralPath "$manifestFile.sig" -Destination (Join-Path $staging 'manifest.json.sig')
        if (Test-Path -LiteralPath $dest) {
            $target = Get-LinkTarget $Current
            if ($target -and ((Split-Path -Leaf $target) -eq $Version)) { Wait-ServiceStopped }
            Remove-Tree $dest
        }
        [IO.Directory]::Move($staging, $dest)
        Switch-Current $Version
    }

    # --- configuration --------------------------------------------------------------------------
    if (-not (Test-Path -LiteralPath $CurrentExe -PathType Leaf)) { Stop-Install "$CurrentExe is missing" }
    $configureArgs = @('-configure', '-config', $Config)
    if ($LicenseKey) { $configureArgs += @('-license-key', $LicenseKey) }
    if ($Endpoint) { $configureArgs += @('-endpoint', $Endpoint) }
    $r = Invoke-Agent -Exe $CurrentExe -Arguments $configureArgs
    if ($r.ExitCode -ne 0) { Stop-Install "configuring $Config failed (exit code $($r.ExitCode))" }
    $licensed = Test-LicenseKeyConfigured
    if (-not $licensed) { Write-InstallLog "warning: no license_key in $Config; pass -LicenseKey" }

    # --- reconcile ------------------------------------------------------------------------------
    # Registers or updates the service (ImagePath, automatic start, recovery) and secures the ProgramData directories.
    $r = Invoke-Agent -Exe $CurrentExe -Arguments @('-reconcile', '-reconcile-context', 'install', '-config', $Config)
    if ($r.ExitCode -ne 0) { Write-InstallLog "warning: reconcile reported errors (exit code $($r.ExitCode), see above)" }
    foreach ($line in $r.Stdout) { if ($line) { Write-Host $line } }
    $restartNeeded = (($r.Stdout -join "`n") -match 'restart-required') -or $install

    # --- service --------------------------------------------------------------------------------
    $svc = Get-AgentService
    if ($null -eq $svc) {
        Write-InstallLog "warning: service $ServiceName is not registered; start the agent with: `"$CurrentExe`" -config `"$Config`""
    } elseif ($NoStart) {
        if ($restartNeeded -and $svc.Status -eq 'Running') { Write-InstallLog "the new release or service definition applies after: Restart-Service $ServiceName" }
    } elseif ($licensed) {
        if ($svc.Status -eq 'Running') {
            Restart-Service -Name $ServiceName -Force
        } else {
            Start-Service -Name $ServiceName
        }
        Write-InstallLog "service $ServiceName (re)started"
    } elseif ($restartNeeded -and $svc.Status -eq 'Running') {
        Restart-Service -Name $ServiceName -Force
    }

    $r = Invoke-Agent -Exe $CurrentExe -Arguments @('-version') -Quiet
    $versionText = (@($r.Stdout) + @($r.Stderr) -join ' ').Trim()
    if (-not $versionText) { $versionText = "openlog-infra-agent $Version" }
    Write-InstallLog "done: $versionText"
} finally {
    try { Remove-Tree $tmp } catch { Write-InstallLog "warning: could not remove ${tmp}: $($_.Exception.Message)" }
}
