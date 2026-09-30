<#
.SYNOPSIS
Tests scripts/install.ps1 against a local release layout.

.DESCRIPTION
Serves a release directory over a local HTTP server (python3 or python, through
BROOOM_DOWNLOAD_BASE and BROOOM_LATEST_URL) and checks a successful install
that resolves the latest tag, a 32-bit PowerShell on 64-bit Windows, a tampered
checksum and an unknown version. CI runs it under Windows PowerShell 5 and
PowerShell 7.

With -DistDir (a GoReleaser dist directory) the real snapshot windows zip and
checksums.txt are used and the installed brooom.exe is executed. Without it a
fake zip holding a stand-in brooom.exe is built, which only tests the script.
#>
param(
  [string]$DistDir
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$installer = Join-Path $PSScriptRoot 'install.ps1'
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("brooom-install-test-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $work | Out-Null
$server = $null

function Fail([string]$message) { throw "FAIL: $message" }

function Get-FreePort {
  $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
  $listener.Start()
  $port = $listener.LocalEndpoint.Port
  $listener.Stop()
  return $port
}

# Lays out <site>/<tag>/<archive>, <site>/<tag>/checksums.txt and
# <site>/latest.json the way GitHub serves them.
function New-Site([string]$site, [string]$version, [string]$arch) {
  $tag = "v$version"
  $archive = "brooom_${version}_windows_${arch}.zip"
  New-Item -ItemType Directory -Path (Join-Path $site $tag) -Force | Out-Null
  $zip = Join-Path $site "$tag/$archive"
  if ($DistDir) {
    $source = Get-ChildItem -Path $DistDir -Filter "brooom_*_windows_${arch}.zip" | Select-Object -First 1
    if (-not $source) { Fail "no windows $arch zip in $DistDir" }
    Copy-Item $source.FullName $zip
  } else {
    $pkg = Join-Path $work 'pkg'
    New-Item -ItemType Directory -Path $pkg -Force | Out-Null
    Set-Content -Path (Join-Path $pkg 'brooom.exe') -Value 'stand-in'
    Compress-Archive -Path (Join-Path $pkg 'brooom.exe') -DestinationPath $zip -Force
  }
  $sum = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLowerInvariant()
  Set-Content -Path (Join-Path $site "$tag/checksums.txt") -Value "$sum  $archive"
  Set-Content -Path (Join-Path $site 'latest.json') -Value ('{"tag_name": "' + $tag + '", "name": "' + $tag + '"}')
}

# Runs the installer in a fresh child process of the current PowerShell edition
# so a throw or exit inside it cannot end this test, and returns its exit code
# and output. Environment changes are per process.
function Invoke-Installer([hashtable]$environment, [string]$command) {
  $psi = [System.Diagnostics.ProcessStartInfo]::new()
  $psi.FileName = (Get-Process -Id $PID).Path
  # With a command the installer is run in that session instead (for example
  # through iex) so what it leaves behind in the caller can be inspected.
  if ($command) {
    $psi.Arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($command))
  } else {
    $psi.Arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $installer + '"'
  }
  $psi.UseShellExecute = $false
  $psi.CreateNoWindow = $true
  $psi.RedirectStandardOutput = $true
  $psi.RedirectStandardError = $true
  # The child's environment lives on its start info, so this process never
  # changes and no case can leak into the next one. Installer variables are
  # cleared unless a case gives them; everything else, including the real
  # processor architecture, is inherited unless a case overrides it.
  foreach ($n in @('BROOOM_VERSION', 'BROOOM_INSTALL_DIR', 'BROOOM_DOWNLOAD_BASE', 'BROOOM_LATEST_URL', 'BROOOM_ADD_TO_PATH')) {
    if ($psi.EnvironmentVariables.ContainsKey($n)) { $psi.EnvironmentVariables.Remove($n) }
  }
  foreach ($n in $environment.Keys) { $psi.EnvironmentVariables[$n] = [string]$environment[$n] }
  # System.Diagnostics.Process instead of "2>&1": Windows PowerShell 5.1 turns the
  # first native stderr line into a terminating NativeCommandError under
  # $ErrorActionPreference = 'Stop', which would abort the negative cases before
  # they report their exit code. Start-Process did not reliably hand the modified
  # environment to the child on PowerShell 7.
  $process = [System.Diagnostics.Process]::Start($psi)
  # Both streams are read concurrently so a full pipe cannot block the child.
  $stdout = $process.StandardOutput.ReadToEndAsync()
  $stderr = $process.StandardError.ReadToEndAsync()
  $process.WaitForExit()
  return @{ Code = $process.ExitCode; Output = [string]($stdout.Result + $stderr.Result) }
}

# Returns a copy of the base environment with the given keys overridden. The
# "+" operator on hashtables throws on duplicate keys, so cases that replace a
# base key (like BROOOM_LATEST_URL) must go through this instead.
function Merge-Env([hashtable]$base, [hashtable]$overrides) {
  $merged = $base.Clone()
  foreach ($k in $overrides.Keys) { $merged[$k] = $overrides[$k] }
  return $merged
}

try {
  $version = '1.2.3'
  $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  $arch = $arch.ToLowerInvariant()
  if ($arch -eq 'x86') { $arch = 'amd64' }
  if ($DistDir) {
    # The snapshot version is whatever GoReleaser stamped into the file name.
    $first = Get-ChildItem -Path $DistDir -Filter "brooom_*_windows_${arch}.zip" | Select-Object -First 1
    if (-not $first) { Fail "no windows $arch zip in $DistDir" }
    $version = ($first.Name -replace '^brooom_', '' -replace "_windows_${arch}\.zip$", '')
  }

  $site = Join-Path $work 'site'
  New-Site $site $version $arch
  $port = Get-FreePort
  $python = Get-Command python3, python -ErrorAction SilentlyContinue | Select-Object -First 1
  if (-not $python) { Fail 'python3 or python is required on PATH to serve the fake release' }
  $server = Start-Process -FilePath $python.Source -ArgumentList @('-m', 'http.server', $port, '--bind', '127.0.0.1', '--directory', $site) -PassThru -WindowStyle Hidden
  $base = "http://127.0.0.1:$port"
  # Wait until the server answers.
  $ready = $false
  for ($i = 0; $i -lt 50 -and -not $ready; $i++) {
    try { Invoke-WebRequest -Uri "$base/latest.json" -UseBasicParsing | Out-Null; $ready = $true } catch { Start-Sleep -Milliseconds 200 }
  }
  if (-not $ready) { Fail "the local release server did not answer at $base within 10 seconds" }

  $baseEnv = @{ BROOOM_DOWNLOAD_BASE = $base; BROOOM_LATEST_URL = "$base/latest.json" }

  Write-Host "== install resolving the latest tag ($($PSVersionTable.PSVersion))"
  $dir = Join-Path $work 'install-latest'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dir })
  if ($result.Code -ne 0) { Fail "install exited $($result.Code): $($result.Output)" }
  $exe = Join-Path $dir 'brooom.exe'
  if (-not (Test-Path $exe)) { Fail "brooom.exe was not installed: $($result.Output)" }
  if ($result.Output -notmatch 'not in your PATH') { Fail "expected the PATH warning: $($result.Output)" }
  if ($DistDir) {
    $reported = & $exe version --format plain
    if ($LASTEXITCODE -ne 0 -or -not $reported) { Fail 'installed brooom.exe does not run' }
    Write-Host "installed binary reports: $reported"
  }

  Write-Host '== PATH hint is usable with iex'
  if ($result.Output -notmatch [regex]::Escape('& ([scriptblock]::Create((irm ')) -or $result.Output -notmatch '-AddToPath') { Fail "expected a scriptblock based -AddToPath hint: $($result.Output)" }

  Write-Host '== iex leaves the caller session untouched'
  $dirI = Join-Path $work 'install-iex'
  $probe = @"
`$ErrorActionPreference = 'Continue'; `$ProgressPreference = 'Continue'
`$vars = @(Get-Variable | ForEach-Object Name)
Get-Content -Raw '$installer' | Invoke-Expression
`$leaks = @()
if (`$ErrorActionPreference -ne 'Continue') { `$leaks += 'ErrorActionPreference' }
if (`$ProgressPreference -ne 'Continue') { `$leaks += 'ProgressPreference' }
`$leaks += @(Get-Variable | ForEach-Object Name | Where-Object { `$vars -notcontains `$_ -and `$_ -ne 'leaks' })
if (Get-Command Get-Arch, Get-LatestTag, Install-Binary -ErrorAction SilentlyContinue) { `$leaks += 'functions' }
if (`$leaks) { Write-Host ('LEAK: ' + (`$leaks -join ',')); exit 3 }
"@
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirI }) $probe
  if ($result.Code -ne 0 -or $result.Output -match 'LEAK') { Fail "iex leaked into the caller ($($result.Code)): $($result.Output)" }
  if (-not (Test-Path (Join-Path $dirI 'brooom.exe'))) { Fail "iex install did not install: $($result.Output)" }

  Write-Host '== hint invocation through a scriptblock'
  $dirS = Join-Path $work 'install-scriptblock'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirS }) "& ([scriptblock]::Create((Get-Content -Raw '$installer')))"
  if ($result.Code -ne 0 -or -not (Test-Path (Join-Path $dirS 'brooom.exe'))) { Fail "scriptblock install failed: $($result.Output)" }

  Write-Host '== upgrade while brooom.exe is running'
  # A handle allowing rename and delete but not writes mimics a running image.
  $exeI = Join-Path $dirI 'brooom.exe'
  $held = [System.IO.File]::Open($exeI, 'Open', 'Read', ([System.IO.FileShare]'Read, Delete'))
  try {
    $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirI })
  } finally { $held.Dispose() }
  if ($result.Code -ne 0) { Fail "upgrade over a running exe failed: $($result.Output)" }
  if (-not (Test-Path $exeI)) { Fail 'brooom.exe missing after the upgrade' }

  Write-Host '== upgrade when brooom.exe cannot be moved'
  $held = [System.IO.File]::Open($exeI, 'Open', 'Read', ([System.IO.FileShare]'Read'))
  try {
    $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirI })
  } finally { $held.Dispose() }
  if ($result.Code -eq 0 -or $result.Output -notmatch 'close any running brooom') { Fail "expected a close-brooom message: $($result.Output)" }
  if (-not (Test-Path $exeI)) { Fail 'the old brooom.exe must stay in place after a failed upgrade' }

  Write-Host '== stale .old is cleaned up'
  Set-Content -Path "$exeI.old" -Value 'stale'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirI })
  if ($result.Code -ne 0 -or (Test-Path "$exeI.old")) { Fail "a stale brooom.exe.old must be removed: $($result.Output)" }

  Write-Host '== 32-bit PowerShell on 64-bit Windows'
  $dir32 = Join-Path $work 'install-wow64'
  $result = Invoke-Installer (Merge-Env $baseEnv @{
      BROOOM_INSTALL_DIR = $dir32; PROCESSOR_ARCHITECTURE = 'x86'; PROCESSOR_ARCHITEW6432 = $arch.ToUpperInvariant()
    })
  if ($result.Code -ne 0 -or -not (Test-Path (Join-Path $dir32 'brooom.exe'))) { Fail "wow64 install failed: $($result.Output)" }

  Write-Host '== unsupported architecture'
  $dirBad = Join-Path $work 'install-x86'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirBad; PROCESSOR_ARCHITECTURE = 'x86' })
  if ($result.Code -eq 0 -or (Test-Path $dirBad)) { Fail "an x86 host must be refused: $($result.Output)" }

  Write-Host '== explicit version'
  $dirV = Join-Path $work 'install-version'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirV; BROOOM_VERSION = "v$version"; BROOOM_LATEST_URL = "$base/missing.json" })
  if ($result.Code -ne 0 -or -not (Test-Path (Join-Path $dirV 'brooom.exe'))) { Fail "versioned install failed: $($result.Output)" }

  Write-Host '== unknown version'
  $dirU = Join-Path $work 'install-unknown'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirU; BROOOM_VERSION = '9.9.9' })
  if ($result.Code -eq 0 -or (Test-Path $dirU)) { Fail "an unknown version must fail without installing: $($result.Output)" }

  Write-Host '== unresolvable latest release'
  $dirL = Join-Path $work 'install-nolatest'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirL; BROOOM_LATEST_URL = "$base/missing.json" })
  if ($result.Code -eq 0 -or (Test-Path $dirL)) { Fail "an unresolvable latest release must fail without installing: $($result.Output)" }

  Write-Host '== tampered checksum'
  $tag = "v$version"
  $sums = Join-Path $site "$tag/checksums.txt"
  $line = Get-Content $sums | Select-Object -First 1
  Set-Content -Path $sums -Value ((('0' * 64)) + '  ' + ($line -split '\s+')[1])
  $dirT = Join-Path $work 'install-tampered'
  $result = Invoke-Installer (Merge-Env $baseEnv @{ BROOOM_INSTALL_DIR = $dirT })
  if ($result.Code -eq 0 -or (Test-Path (Join-Path $dirT 'brooom.exe'))) { Fail "a checksum mismatch must abort the install: $($result.Output)" }
  if ($result.Output -notmatch 'Checksum mismatch') { Fail "expected a checksum mismatch message: $($result.Output)" }

  Write-Host 'install.ps1 tests passed'
} finally {
  if ($server -and -not $server.HasExited) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
