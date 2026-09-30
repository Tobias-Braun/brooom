<#
.SYNOPSIS
Installs the latest or a chosen Brooom release.

.DESCRIPTION
  irm https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1 | iex

Environment: BROOOM_VERSION, BROOOM_INSTALL_DIR, BROOOM_DOWNLOAD_BASE,
BROOOM_LATEST_URL (see scripts/install.sh; here it is a URL answering with the
GitHub releases/latest JSON, of which only tag_name is read). Resolving the
latest release calls the GitHub API, which allows 60 unauthenticated requests
per hour and IP; set GITHUB_TOKEN to authenticate (sent only to the default
GitHub API URL) or pin BROOOM_VERSION. The zip name must match
archives.name_template in .goreleaser.yaml. The checksum is verified before
anything is extracted. The user PATH is only changed with -AddToPath.
#>
param(
  [switch]$AddToPath
)

$ErrorActionPreference = 'Stop'
# Invoke-WebRequest is very slow with the progress bar on Windows PowerShell 5.
$ProgressPreference = 'SilentlyContinue'

$repoUrl = 'https://github.com/Tobias-Braun/brooom/releases'
$downloadBase = if ($env:BROOOM_DOWNLOAD_BASE) { $env:BROOOM_DOWNLOAD_BASE } else { "$repoUrl/download" }
$latestUrl = if ($env:BROOOM_LATEST_URL) { $env:BROOOM_LATEST_URL } else { "https://api.github.com/repos/Tobias-Braun/brooom/releases/latest" }

function Get-Arch {
  # A 32-bit PowerShell on 64-bit Windows reports PROCESSOR_ARCHITECTURE=x86
  # and keeps the real architecture in PROCESSOR_ARCHITEW6432.
  $raw = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  switch ($raw) {
    'AMD64' { return 'amd64' }
    'ARM64' { return 'arm64' }
    default { throw "Unsupported architecture '$raw'; supported: AMD64, ARM64" }
  }
}

function Get-LatestTag {
  # The releases API answers with plain JSON on every PowerShell edition. The
  # releases/latest redirect is not used: reading it needs different flags on
  # Windows PowerShell 5 and PowerShell 7 and fails on the latter.
  try {
    # Windows PowerShell 5 may default to protocols GitHub no longer accepts.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  } catch {}
  $headers = @{ Accept = 'application/vnd.github+json' }
  # The token must not leak to an overridden URL.
  if ($env:GITHUB_TOKEN -and -not $env:BROOOM_LATEST_URL) { $headers['Authorization'] = "Bearer $env:GITHUB_TOKEN" }
  try {
    $release = Invoke-RestMethod -Uri $latestUrl -Headers $headers
  } catch {
    throw "Cannot resolve the latest release from $latestUrl ($($_.Exception.Message)); the GitHub API allows 60 unauthenticated requests per hour, so set GITHUB_TOKEN or BROOOM_VERSION"
  }
  $tag = [string]$release.tag_name
  if (-not $tag) { throw "Cannot resolve the latest release from $latestUrl (no tag_name); set BROOOM_VERSION" }
  return $tag
}

$arch = Get-Arch
if ($env:BROOOM_VERSION) { $tag = 'v' + $env:BROOOM_VERSION.TrimStart('v') } else { $tag = Get-LatestTag }
$version = $tag.TrimStart('v')
$archive = "brooom_${version}_windows_${arch}.zip"

$installDir = if ($env:BROOOM_INSTALL_DIR) { $env:BROOOM_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\brooom' }

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("brooom-install-" + [System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Installing brooom $version for windows/$arch"
  Invoke-WebRequest -Uri "$downloadBase/$tag/$archive" -OutFile (Join-Path $tmp $archive) -UseBasicParsing
  Invoke-WebRequest -Uri "$downloadBase/$tag/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive } | Select-Object -First 1
  if (-not $line) { throw "No checksum for $archive in checksums.txt" }
  $expected = ($line -split '\s+')[0]
  $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $archive)).Hash
  if ($expected.ToLowerInvariant() -ne $actual.ToLowerInvariant()) {
    throw "Checksum mismatch for $archive (expected $expected, got $actual); aborting"
  }

  Expand-Archive -Path (Join-Path $tmp $archive) -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $installDir -Force | Out-Null
  Copy-Item -Path (Join-Path $tmp 'brooom.exe') -Destination (Join-Path $installDir 'brooom.exe') -Force
  Write-Host "Installed $(Join-Path $installDir 'brooom.exe')"
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$onPath = ($userPath -split ';') -contains $installDir
if (-not $onPath) {
  if ($AddToPath) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$installDir", 'User')
    Write-Host "Added $installDir to your user PATH. Restart your terminal to pick it up."
  } else {
    Write-Host "Warning: $installDir is not in your PATH. Add it with:"
    Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$installDir', 'User')"
    Write-Host "or re-run this script with -AddToPath."
  }
}
