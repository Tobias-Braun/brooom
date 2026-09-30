<#
.SYNOPSIS
Installs the latest (or a chosen) Brooom release from GitHub releases.

.DESCRIPTION
  irm https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1 | iex

Environment: BROOOM_VERSION, BROOOM_INSTALL_DIR, BROOOM_DOWNLOAD_BASE,
BROOOM_LATEST_URL (see scripts/install.sh). The zip name must match
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
$latestUrl = if ($env:BROOOM_LATEST_URL) { $env:BROOOM_LATEST_URL } else { "$repoUrl/latest" }

function Get-Arch {
  switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { return 'amd64' }
    'ARM64' { return 'arm64' }
    default { throw "Unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'; supported: AMD64, ARM64" }
  }
}

function Get-LatestTag {
  # The releases/latest URL redirects to .../tag/<tag>; read the redirect
  # instead of following it.
  try {
    $response = Invoke-WebRequest -Uri $latestUrl -MaximumRedirection 0 -UseBasicParsing -ErrorAction SilentlyContinue
  } catch {
    $response = $_.Exception.Response
  }
  $location = $null
  if ($response -and $response.Headers) { $location = [string]($response.Headers['Location'] | Select-Object -First 1) }
  if (-not $location) { throw "Cannot resolve the latest release from $latestUrl; set BROOOM_VERSION" }
  return ($location.TrimEnd('/') -split '/')[-1]
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
