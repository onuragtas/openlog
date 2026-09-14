#Requires -Version 7.0
<#
.SYNOPSIS
    Runs samples/OpenLog.AspNetFramework.Sample (.NET Framework 4.8 / ASP.NET 4.x) under IIS Express or IIS and checks
    the telemetry OpenLog.Agent exports (CI job dotnet-agent-netfx on windows-latest).

.DESCRIPTION
    1. builds the sample with all dependencies copied to its output and binding redirects generated
       (OpenLog.AspNetFramework.Sample.dll.config);
    2. builds test/OpenLog.NetFx.SmokeTest (OTLP capture server + requests + span assertions);
    3. lays out a site: bin/, Global.asax and a web.config generated from Web.config.template (appSettings pointing
       OPENLOG_ENDPOINT at the capture server, the binding redirects merged into <runtime>);
    4. hosts the site under IIS Express when installed, otherwise under full IIS (enabled with DISM, site + .NET v4.0
       integrated application pool created with appcmd);
    5. runs the smoke test; on failure prints web.config, bin/, server logs and Windows event log entries;
    6. always stops the server.

.EXAMPLE
    pwsh agents/dotnet/test/netfx/run-iis.ps1
.EXAMPLE
    pwsh agents/dotnet/test/netfx/run-iis.ps1 -Server iis -SitePort 8090
.EXAMPLE
    # site layout + web.config generation only, from an existing build output (works on Linux/macOS)
    pwsh run-iis.ps1 -BinDir /artifacts/bin/OpenLog.AspNetFramework.Sample/release -LayoutOnly
#>
[CmdletBinding()]
param(
    [ValidateSet('auto', 'iisexpress', 'iis')]
    [string]$Server = 'auto',
    [int]$SitePort = 8085,
    [int]$CapturePort = 4319,
    [string]$LicenseKey = 'netfx-smoke-license-key',
    [string]$WorkDir = (Join-Path ($env:RUNNER_TEMP ?? [IO.Path]::GetTempPath()) 'openlog-netfx'),
    # an existing build output of the sample (skips the sample build)
    [string]$BinDir = '',
    # build/lay out the site and stop (no server, no smoke test)
    [switch]$LayoutOnly,
    [int]$ReadyTimeoutSeconds = 240,
    [int]$SpanTimeoutSeconds = 90
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$DotnetDir = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$SampleProject = Join-Path $DotnetDir 'samples/OpenLog.AspNetFramework.Sample/OpenLog.AspNetFramework.Sample.csproj'
$SmokeProject = Join-Path $DotnetDir 'test/OpenLog.NetFx.SmokeTest/OpenLog.NetFx.SmokeTest.csproj'
$SiteName = 'openlog-netfx'
$SiteDir = Join-Path $WorkDir 'site'
$LogDir = Join-Path $WorkDir 'logs'
$SiteUrl = "http://localhost:$SitePort"
$BuildProps = @('-p:CopyLocalLockFileAssemblies=true', '-p:AutoGenerateBindingRedirects=true', '-p:GenerateBindingRedirectsOutputType=true')
$RequiredAssemblies = @(
    'OpenLog.AspNetFramework.Sample.dll', 'OpenLog.Agent.dll', 'OpenTelemetry.dll', 'OpenTelemetry.Api.dll',
    'OpenTelemetry.Exporter.OpenTelemetryProtocol.dll', 'OpenTelemetry.Instrumentation.AspNet.dll',
    'OpenTelemetry.Instrumentation.AspNet.TelemetryHttpModule.dll', 'System.Diagnostics.DiagnosticSource.dll'
)

$script:Clock = [Diagnostics.Stopwatch]::StartNew()
$script:StartTime = Get-Date
$script:StepNo = 0
$script:HostMode = ''
$script:IisExpress = $null
$script:IisSiteId = ''

function Write-Step([string]$Title) {
    $script:StepNo++
    Write-Host ''
    Write-Host ('==== [{0:mm\:ss}] step {1}: {2} ====' -f $script:Clock.Elapsed, $script:StepNo, $Title) -ForegroundColor Cyan
}

function Write-Info([string]$Message) { Write-Host "  $Message" }

function Write-Section([string]$Title) { Write-Host ''; Write-Host "---- $Title ----" -ForegroundColor Yellow }

# Runs a native command, echoing it; throws unless the exit code is allowed.
function Invoke-Native([string]$File, [string[]]$Arguments, [int[]]$AllowedExitCodes = @(0)) {
    Write-Info "> $File $($Arguments -join ' ')"
    & $File @Arguments | Out-Host
    $code = $LASTEXITCODE
    if ($AllowedExitCodes -notcontains $code) { throw "$File exited with code $code" }
    return $code
}

# The value of an MSBuild property after evaluation (honours OPENLOG_DOTNET_ARTIFACTS / ArtifactsPath).
function Get-MSBuildProperty([string]$Project, [string]$Name, [string[]]$ExtraProps = @()) {
    $value = & dotnet msbuild $Project "-getProperty:$Name" -p:Configuration=Release @ExtraProps
    if ($LASTEXITCODE -ne 0) { throw "dotnet msbuild -getProperty:$Name failed for $Project" }
    return ($value | Out-String).Trim()
}

function Test-Port([int]$Port) {
    $client = [Net.Sockets.TcpClient]::new()
    try {
        return $client.ConnectAsync('127.0.0.1', $Port).Wait(1000) -and $client.Connected
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

function Show-FileTail([string]$Path, [int]$Lines = 60) {
    if (-not (Test-Path $Path)) { Write-Info "(missing: $Path)"; return }
    foreach ($f in @(Get-ChildItem -Path $Path -File -Recurse -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 4)) {
        Write-Section "$($f.FullName) (last $Lines lines)"
        Get-Content -Path $f.FullName -Tail $Lines | Out-Host
    }
}

function Build-Sample {
    Write-Step 'build the .NET Framework 4.8 sample (dependencies copied locally, binding redirects generated)'
    Invoke-Native 'dotnet' (@('build', $SampleProject, '-c', 'Release', '-nologo') + $BuildProps) | Out-Null
    $dir = Get-MSBuildProperty $SampleProject 'TargetDir' $BuildProps
    Write-Info "sample output: $dir"
    return $dir
}

function Build-SmokeTest {
    Write-Step 'build the smoke test (OTLP capture server + assertions, net8.0)'
    Invoke-Native 'dotnet' @('build', $SmokeProject, '-c', 'Release', '-nologo') | Out-Null
    $dll = Get-MSBuildProperty $SmokeProject 'TargetPath'
    if (-not (Test-Path $dll)) { throw "smoke test assembly not found: $dll" }
    Write-Info "smoke test: $dll"
    return $dll
}

function New-SiteLayout([string]$SampleBin) {
    Write-Step "lay out the site in $SiteDir"
    foreach ($name in $RequiredAssemblies) {
        if (-not (Test-Path (Join-Path $SampleBin $name))) { throw "the sample build output $SampleBin lacks $name" }
    }
    $bindingConfig = Join-Path $SampleBin 'OpenLog.AspNetFramework.Sample.dll.config'
    if (-not (Test-Path $bindingConfig)) { throw "no generated binding redirects ($bindingConfig); build with $($BuildProps -join ' ')" }

    if (Test-Path $SiteDir) { Remove-Item -Path $SiteDir -Recurse -Force }
    $bin = Join-Path $SiteDir 'bin'
    New-Item -ItemType Directory -Path $bin -Force | Out-Null
    New-Item -ItemType Directory -Path $LogDir -Force | Out-Null
    Get-ChildItem -Path $SampleBin -File | Where-Object { $_.Extension -in '.dll', '.pdb' } | Copy-Item -Destination $bin
    Copy-Item -Path (Join-Path $PSScriptRoot 'Global.asax') -Destination $SiteDir
    Write-Info ("bin/: {0} assemblies" -f @(Get-ChildItem -Path $bin -Filter '*.dll').Count)

    $doc = [Xml.XmlDocument]::new()
    $doc.PreserveWhitespace = $false
    $doc.Load((Join-Path $PSScriptRoot 'Web.config.template'))
    $settings = @{ OPENLOG_ENDPOINT = "http://127.0.0.1:$CapturePort"; OPENLOG_LICENSE_KEY = $LicenseKey }
    foreach ($key in $settings.Keys) {
        $node = $doc.SelectSingleNode("/configuration/appSettings/add[@key='$key']")
        if ($null -eq $node) { throw "Web.config.template has no appSettings entry $key" }
        $node.SetAttribute('value', $settings[$key])
        Write-Info "appSettings $key = $($settings[$key])"
    }

    $redirects = [Xml.XmlDocument]::new()
    $redirects.Load($bindingConfig)
    $runtime = $redirects.SelectSingleNode('/configuration/runtime')
    if ($null -eq $runtime) { throw "$bindingConfig has no <runtime> element" }
    $asmv1 = 'urn:schemas-microsoft-com:asm.v1'
    $dependent = @($runtime.GetElementsByTagName('dependentAssembly', $asmv1))
    if ($dependent.Count -eq 0) { throw "$bindingConfig contains no binding redirects" }
    foreach ($d in $dependent) {
        $identity = $d.GetElementsByTagName('assemblyIdentity', $asmv1)[0]
        $redirect = $d.GetElementsByTagName('bindingRedirect', $asmv1)[0]
        Write-Info ("binding redirect {0} {1} -> {2}" -f $identity.GetAttribute('name'), $redirect.GetAttribute('oldVersion'), $redirect.GetAttribute('newVersion'))
    }
    foreach ($old in @($doc.SelectNodes('/configuration/runtime'))) { [void]$old.ParentNode.RemoveChild($old) }
    [void]$doc.DocumentElement.AppendChild($doc.ImportNode($runtime, $true))

    $webConfig = Join-Path $SiteDir 'Web.config'
    $xmlSettings = [Xml.XmlWriterSettings]::new()
    $xmlSettings.Indent = $true
    $xmlSettings.Encoding = [Text.UTF8Encoding]::new($false)
    $writer = [Xml.XmlWriter]::Create($webConfig, $xmlSettings)
    try { $doc.Save($writer) } finally { $writer.Dispose() }
    Write-Info "wrote $webConfig ($($dependent.Count) binding redirects)"
}

function Find-IisExpress {
    foreach ($root in @($env:ProgramFiles, ${env:ProgramFiles(x86)})) {
        if (-not $root) { continue }
        $exe = Join-Path $root 'IIS Express\iisexpress.exe'
        if (Test-Path $exe) { return $exe }
    }
    return $null
}

function Wait-SitePort([int]$TimeoutSeconds = 60) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-Port $SitePort) { Write-Info "port $SitePort accepts connections"; return }
        if ($script:IisExpress -and $script:IisExpress.HasExited) { throw "IIS Express exited with code $($script:IisExpress.ExitCode)" }
        Start-Sleep -Milliseconds 500
    }
    throw "nothing listens on port $SitePort after ${TimeoutSeconds}s"
}

function Start-IisExpress([string]$Exe) {
    $script:HostMode = 'iisexpress'
    Write-Step "host the site under IIS Express ($Exe)"
    $iisArgs = @("/path:`"$SiteDir`"", "/port:$SitePort", '/systray:false', '/trace:error')
    Write-Info "> $Exe $($iisArgs -join ' ')"
    $script:IisExpress = Start-Process -FilePath $Exe -ArgumentList $iisArgs -PassThru -NoNewWindow `
        -RedirectStandardOutput (Join-Path $LogDir 'iisexpress.out.log') -RedirectStandardError (Join-Path $LogDir 'iisexpress.err.log')
    Write-Info "IIS Express pid $($script:IisExpress.Id)"
    Wait-SitePort
}

function Start-FullIis {
    $script:HostMode = 'iis'
    Write-Step 'host the site under IIS (IIS Express not installed or -Server iis)'
    $appcmd = Join-Path $env:windir 'System32\inetsrv\appcmd.exe'
    $aspnet = Join-Path $env:windir 'System32\inetsrv\webengine4.dll'
    if (-not (Test-Path $appcmd) -or -not (Test-Path $aspnet)) {
        Write-Info 'enabling IIS + ASP.NET 4.8 with DISM (a few minutes)'
        $features = @('IIS-WebServerRole', 'IIS-WebServer', 'IIS-CommonHttpFeatures', 'IIS-StaticContent', 'IIS-DefaultDocument',
            'IIS-HttpErrors', 'IIS-HealthAndDiagnostics', 'IIS-HttpLogging', 'IIS-ApplicationDevelopment', 'IIS-NetFxExtensibility45',
            'IIS-ISAPIExtensions', 'IIS-ISAPIFilter', 'IIS-ASPNET45', 'NetFx4Extended-ASPNET45')
        $dismArgs = @('/online', '/enable-feature', '/all', '/norestart', '/quiet') + ($features | ForEach-Object { "/featurename:$_" })
        # 3010: success, restart required (not needed for IIS)
        Invoke-Native 'dism.exe' $dismArgs @(0, 3010) | Out-Null
    } else {
        Write-Info 'IIS and ASP.NET 4.x are already installed'
    }
    foreach ($svc in 'WAS', 'W3SVC') { Start-Service -Name $svc; Write-Info "service ${svc}: $((Get-Service -Name $svc).Status)" }

    Invoke-Native 'icacls.exe' @($WorkDir, '/grant', 'IIS_IUSRS:(OI)(CI)RX', 'IUSR:(OI)(CI)RX', '/T', '/Q') | Out-Null
    & $appcmd delete site $SiteName 2>&1 | Out-Null
    & $appcmd delete apppool $SiteName 2>&1 | Out-Null
    Invoke-Native $appcmd @('add', 'apppool', "/name:$SiteName", '/managedRuntimeVersion:v4.0', '/managedPipelineMode:Integrated') | Out-Null
    Invoke-Native $appcmd @('add', 'site', "/name:$SiteName", "/physicalPath:$SiteDir", "/bindings:http/*:${SitePort}:") | Out-Null
    Invoke-Native $appcmd @('set', 'app', "$SiteName/", "/applicationPool:$SiteName") | Out-Null
    # "already started" is fine
    & $appcmd start site $SiteName | Out-Host
    $script:IisSiteId = (& $appcmd list site $SiteName /text:id | Out-String).Trim()
    Write-Info "IIS site $SiteName id $($script:IisSiteId), application pool $SiteName (v4.0, Integrated)"
    Wait-SitePort
}

function Stop-SiteServer {
    Write-Step "stop the server ($(if ($script:HostMode) { $script:HostMode } else { 'none started' }))"
    try {
        if ($script:HostMode -eq 'iisexpress' -and $script:IisExpress) {
            if (-not $script:IisExpress.HasExited) { Stop-Process -Id $script:IisExpress.Id -Force; $script:IisExpress.WaitForExit(10000) | Out-Null }
            Write-Info 'IIS Express stopped'
        } elseif ($script:HostMode -eq 'iis') {
            $appcmd = Join-Path $env:windir 'System32\inetsrv\appcmd.exe'
            & $appcmd stop site $SiteName 2>&1 | Out-Host
            & $appcmd delete site $SiteName 2>&1 | Out-Host
            & $appcmd stop apppool $SiteName 2>&1 | Out-Host
            & $appcmd delete apppool $SiteName 2>&1 | Out-Host
            Write-Info 'IIS site and application pool removed'
        }
    } catch {
        Write-Warning "stopping the server failed: $_"
    }
}

function Show-Diagnostics {
    Write-Step 'diagnostics'
    try {
        $webConfig = Join-Path $SiteDir 'Web.config'
        Write-Section $webConfig
        if (Test-Path $webConfig) { Get-Content $webConfig | Out-Host } else { Write-Info '(not generated)' }
        Write-Section "$SiteDir\bin"
        if (Test-Path (Join-Path $SiteDir 'bin')) {
            Get-ChildItem (Join-Path $SiteDir 'bin') | Sort-Object Name | ForEach-Object {
                $v = if ($_.Extension -eq '.dll') { $_.VersionInfo.FileVersion } else { '' }
                Write-Info ('{0,-70} {1,10} {2}' -f $_.Name, $_.Length, $v)
            }
        }
        if ($script:HostMode -eq 'iisexpress') {
            Show-FileTail (Join-Path $LogDir 'iisexpress.out.log') 100
            Show-FileTail (Join-Path $LogDir 'iisexpress.err.log') 100
            $docs = [Environment]::GetFolderPath('MyDocuments')
            Show-FileTail (Join-Path $docs 'IISExpress\Logs')
            Show-FileTail (Join-Path $docs 'IISExpress\TraceLogFiles') 80
        } elseif ($script:HostMode -eq 'iis') {
            Show-FileTail (Join-Path $env:SystemDrive "inetpub\logs\LogFiles\W3SVC$($script:IisSiteId)")
            Show-FileTail (Join-Path $env:windir 'System32\LogFiles\HTTPERR') 30
            $appcmd = Join-Path $env:windir 'System32\inetsrv\appcmd.exe'
            Write-Section 'appcmd list site / apppool'
            & $appcmd list site 2>&1 | Out-Host
            & $appcmd list apppool 2>&1 | Out-Host
        }
        if ($IsWindows) {
            Write-Section 'Windows event log (Application + System since the start of this run: ASP.NET, IIS, WAS, .NET Runtime, errors)'
            $events = @(Get-WinEvent -FilterHashtable @{ LogName = 'Application', 'System'; StartTime = $script:StartTime } -ErrorAction SilentlyContinue |
                Where-Object { $_.ProviderName -match 'ASP\.NET|IIS|W3SVC|WAS|\.NET Runtime|Application Error|Windows Error Reporting' -or $_.Level -le 2 } |
                Select-Object -First 40)
            if ($events.Count -eq 0) { Write-Info '(no matching events)' }
            foreach ($e in $events) {
                Write-Host ("[{0:HH:mm:ss}] {1} {2} id={3} level={4}" -f $e.TimeCreated, $e.LogName, $e.ProviderName, $e.Id, $e.LevelDisplayName)
                Write-Host (($e.Message ?? '') -split "`n" | Select-Object -First 30 | Out-String)
            }
        }
    } catch {
        Write-Warning "collecting diagnostics failed: $_"
    }
}

# ---- main ----
Write-Host "openlog .NET Framework / ASP.NET 4.x smoke test: server=$Server site=$SiteUrl capture=127.0.0.1:$CapturePort workdir=$WorkDir"
if (-not $IsWindows -and -not $LayoutOnly) { throw 'IIS / IIS Express need Windows (use -LayoutOnly elsewhere)' }

$failed = $false
try {
    $sampleBin = if ($BinDir) { Write-Info "using the existing sample build output $BinDir"; (Resolve-Path $BinDir).Path } else { Build-Sample }
    if (-not $LayoutOnly) { $smokeDll = Build-SmokeTest }
    New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null
    New-SiteLayout $sampleBin
    if ($LayoutOnly) {
        Write-Section (Join-Path $SiteDir 'Web.config')
        Get-Content (Join-Path $SiteDir 'Web.config') | Out-Host
        Write-Host ''
        Write-Host "LAYOUT ONLY: site laid out in $SiteDir (no server started)" -ForegroundColor Green
        exit 0
    }

    if (Test-Port $CapturePort) { throw "port $CapturePort (OTLP capture) is already in use" }
    if (Test-Port $SitePort) { throw "port $SitePort (site) is already in use" }
    $iisExpress = if ($Server -ne 'iis') { Find-IisExpress } else { $null }
    if ($Server -eq 'iisexpress' -and -not $iisExpress) { throw 'IIS Express is not installed (-Server iisexpress)' }
    if ($iisExpress) { Start-IisExpress $iisExpress } else { Start-FullIis }
    Write-Host "HOST: $($script:HostMode)" -ForegroundColor Green

    Write-Step "smoke test against $SiteUrl ($($script:HostMode))"
    $smokeArgs = @($smokeDll, '--site', $SiteUrl, '--capture-port', "$CapturePort", '--license-key', $LicenseKey,
        '--service-name', 'legacy-web', '--ready-timeout', "$ReadyTimeoutSeconds", '--span-timeout', "$SpanTimeoutSeconds")
    Write-Info "> dotnet $($smokeArgs -join ' ')"
    & dotnet @smokeArgs | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "smoke test failed (exit code $LASTEXITCODE)" }
} catch {
    $failed = $true
    Write-Host ''
    Write-Host "ERROR: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host $_.ScriptStackTrace
} finally {
    if ($failed) { Show-Diagnostics }
    if (-not $LayoutOnly) { Stop-SiteServer }
}

Write-Host ''
if ($failed) {
    Write-Host ("FAIL: .NET Framework / ASP.NET 4.x smoke test under {0} ({1:mm\:ss})" -f $(if ($script:HostMode) { $script:HostMode } else { 'no server' }), $script:Clock.Elapsed) -ForegroundColor Red
    exit 1
}
Write-Host ("PASS: .NET Framework / ASP.NET 4.x smoke test under {0} ({1:mm\:ss})" -f $script:HostMode, $script:Clock.Elapsed) -ForegroundColor Green
exit 0
