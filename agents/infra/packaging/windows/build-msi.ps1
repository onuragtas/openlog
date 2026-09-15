<#
Builds the openlog infrastructure agent MSI with WiX Toolset v5 (installed on demand as a .NET global tool).

  ./build-msi.ps1 -Version 0.5.0 -Arch amd64 -Exe dist\openlog-infra-agent.exe `
      -SourceDir dist\openlog-infra-agent_0.5.0_windows_amd64 -Out dist\openlog-infra-agent_0.5.0_windows_amd64.msi `
      [-ManifestDir dist\embedded-manifest]

-SourceDir is the extracted release top directory (LICENSE, README.md, packaging\config.example.yaml).
-Arch amd64 (x64) or arm64: the exe must be built for that architecture. An arm64 MSI builds on any Windows host but
installs only on Windows on ARM.
-ManifestDir holds manifest.json and manifest.json.sig of -Version (signed; listing the windows zip archives, like the
manifest deb/rpm embed). It is installed into versions\<v>\ so that a backend-ordered rollback from this MSI-installed
version passes rule 5 (rollback_floor). Without it the MSI builds with a warning. Release builds always pass it.
The MSI ProductVersion is the numeric major.minor.patch of -Version (a pre-release suffix is stripped).
Requires the .NET SDK (dotnet) on PATH.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Arch = 'amd64',
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$SourceDir,
    [Parameter(Mandatory = $true)][string]$Out,
    [string]$ManifestDir
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$WixVersion = '5.0.2'
$UtilExtension = "WixToolset.Util.wixext/$WixVersion"

function Write-BuildLog([string]$Message) { Write-Host "build-msi: $Message" }

function Invoke-Native([string]$File, [string[]]$Arguments) {
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & $File @Arguments 2>&1 | ForEach-Object { $_.ToString() }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    return [pscustomobject]@{ ExitCode = $code; Output = @($output) }
}

switch ($Arch) {
    'amd64' { $wixArch = 'x64'; $utilCA = 'Wix4UtilCA_X64'; $peMachine = 0x8664 }
    'arm64' { $wixArch = 'arm64'; $utilCA = 'Wix4UtilCA_A64'; $peMachine = 0xAA64 }
    default { throw "build-msi: unsupported -Arch $Arch (amd64, arm64)" }
}

$Version = $Version -replace '^v', ''
if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$') {
    throw "build-msi: -Version $Version is not a SemVer version"
}
$Version = $Version -replace '\+.*$', ''
$major = [int64]$Matches[1]; $minor = [int64]$Matches[2]; $patch = [int64]$Matches[3]
if ($major -gt 255 -or $minor -gt 255 -or $patch -gt 65535) {
    throw "build-msi: $Version does not fit an MSI ProductVersion (max 255.255.65535)"
}
$msiVersion = "$major.$minor.$patch"

$Exe = (Resolve-Path -LiteralPath $Exe).Path
$SourceDir = (Resolve-Path -LiteralPath $SourceDir).Path.TrimEnd('\', '/')
foreach ($f in @('LICENSE', 'README.md', 'packaging\config.example.yaml')) {
    if (-not (Test-Path -LiteralPath (Join-Path $SourceDir $f) -PathType Leaf)) { throw "build-msi: $SourceDir has no $f" }
}
# The exe must match -Arch (PE header machine type), otherwise the MSI would install a binary that cannot start.
$bytes = [IO.File]::ReadAllBytes($Exe)
$peOffset = [BitConverter]::ToInt32($bytes, 0x3C)
$machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
if ($machine -ne $peMachine) { throw ("build-msi: {0} is built for PE machine 0x{1:X4}, not {2}" -f $Exe, $machine, $Arch) }

$manifestDefines = @()
if ($ManifestDir) {
    $ManifestDir = (Resolve-Path -LiteralPath $ManifestDir).Path.TrimEnd('\', '/')
    foreach ($f in @('manifest.json', 'manifest.json.sig')) {
        if (-not (Test-Path -LiteralPath (Join-Path $ManifestDir $f) -PathType Leaf)) { throw "build-msi: $ManifestDir has no $f" }
    }
    $embedded = [IO.File]::ReadAllText((Join-Path $ManifestDir 'manifest.json')) | ConvertFrom-Json
    if ($embedded.product -ne 'openlog' -or [string]$embedded.version -ne $Version) {
        throw "build-msi: $ManifestDir\manifest.json is for $($embedded.product) $($embedded.version), not openlog $Version"
    }
    if (-not $embedded.compatibility.rollback_floor) { throw "build-msi: $ManifestDir\manifest.json has no compatibility.rollback_floor" }
    $zipName = "openlog-infra-agent_${Version}_windows_$Arch.zip"
    if (-not (@($embedded.artifacts) | Where-Object { $_.name -eq $zipName })) { throw "build-msi: $ManifestDir\manifest.json does not list $zipName" }
    $manifestDefines = @('-d', "ManifestDir=$ManifestDir")
    Write-BuildLog "embedding the signed manifest of $Version (rollback_floor $($embedded.compatibility.rollback_floor))"
} else {
    Write-Warning "build-msi: no -ManifestDir: versions\$Version\ gets no manifest.json, so a backend-ordered rollback from this MSI-installed version is refused (rule 5)"
}
$outDir = Split-Path -Parent $Out
if ($outDir -and -not (Test-Path -LiteralPath $outDir)) { New-Item -ItemType Directory -Path $outDir | Out-Null }
$wxs = Join-Path $PSScriptRoot 'openlog-infra-agent.wxs'

# --- WiX tool and extension ---------------------------------------------------------------------
if (-not (Get-Command dotnet -ErrorAction SilentlyContinue)) { throw 'build-msi: the .NET SDK (dotnet) is required' }
$toolsDir = Join-Path $HOME '.dotnet\tools'
if ((Test-Path -LiteralPath $toolsDir) -and (($env:PATH -split ';') -notcontains $toolsDir)) { $env:PATH = "$toolsDir;$env:PATH" }

$haveWix = $false
if (Get-Command wix -ErrorAction SilentlyContinue) {
    $r = Invoke-Native 'wix' @('--version')
    $haveWix = ($r.ExitCode -eq 0) -and (($r.Output -join '') -match ('^' + [regex]::Escape($WixVersion) + '([+-]|$)'))
}
if (-not $haveWix) {
    Write-BuildLog "installing wix $WixVersion"
    $r = Invoke-Native 'dotnet' @('tool', 'update', '--global', 'wix', '--version', $WixVersion)
    if ($r.ExitCode -ne 0) {
        $r.Output | Write-Host
        throw "build-msi: installing the wix $WixVersion .NET tool failed"
    }
    if (($env:PATH -split ';') -notcontains $toolsDir) { $env:PATH = "$toolsDir;$env:PATH" }
}

$r = Invoke-Native 'wix' @('extension', 'list', '--global')
if (($r.Output -join "`n") -notmatch ('WixToolset\.Util\.wixext\s+' + [regex]::Escape($WixVersion))) {
    Write-BuildLog "adding $UtilExtension"
    $r = Invoke-Native 'wix' @('extension', 'add', '--global', $UtilExtension)
    if ($r.ExitCode -ne 0) {
        $r.Output | Write-Host
        throw "build-msi: adding $UtilExtension failed"
    }
}

# --- build --------------------------------------------------------------------------------------
Write-BuildLog "building $Out (version $Version, ProductVersion $msiVersion, $wixArch)"
$r = Invoke-Native 'wix' (@(
    'build', '-arch', $wixArch, '-ext', $UtilExtension,
    '-d', "Version=$Version", '-d', "MsiVersion=$msiVersion", '-d', "Exe=$Exe", '-d', "SourceDir=$SourceDir",
    '-d', "UtilCA=$utilCA"
) + $manifestDefines + @('-o', $Out, $wxs))
$r.Output | Write-Host
if ($r.ExitCode -ne 0) { throw "build-msi: wix build failed (exit code $($r.ExitCode))" }
if (-not (Test-Path -LiteralPath $Out -PathType Leaf)) { throw "build-msi: wix build did not produce $Out" }
Write-BuildLog "built $Out ($((Get-Item -LiteralPath $Out).Length) bytes)"
