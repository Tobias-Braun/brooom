<#
.SYNOPSIS
Installs the latest or a chosen Brooom release.

.DESCRIPTION
  irm https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1 | iex

Environment: BROOOM_VERSION, BROOOM_INSTALL_DIR, BROOOM_DOWNLOAD_BASE,
BROOOM_ADD_TO_PATH=1 (same as -AddToPath), BROOOM_LATEST_URL (see
scripts/install.sh; here it is a URL answering with the GitHub releases/latest
JSON, of which only tag_name is read). Resolving the latest release calls the
GitHub API, which allows 60 unauthenticated requests per hour and IP; set
GITHUB_TOKEN to authenticate (sent only to the default GitHub API URL) or pin
BROOOM_VERSION. The zip name must match archives.name_template in
.goreleaser.yaml. The checksum is verified before anything is extracted. The
user PATH is only changed with -AddToPath or BROOOM_ADD_TO_PATH=1. With iex
there is no way to pass a switch, so use:
  & ([scriptblock]::Create((irm <url>))) -AddToPath

Next to brooom.exe it installs the short command br.exe, a hardlink to
brooom.exe, but only when br is free. A br that already exists (a file,
another command on PATH, a PowerShell function or alias such as broot's) is
never touched: the installer says why br was skipped and brooom works as usual.

The whole script runs in a child scope so that neither its preferences nor its
variables and functions leak into the session that ran it through iex.
#>

& {
  # The switch is read from $args instead of a param block: a param block would
  # define $AddToPath in the caller's scope when the text is run through iex.
  param([object[]]$Arguments)
  $AddToPath = ($Arguments -contains '-AddToPath') -or ($env:BROOOM_ADD_TO_PATH -eq '1')

  $ErrorActionPreference = 'Stop'
  # Invoke-WebRequest is very slow with the progress bar on Windows PowerShell 5.
  $ProgressPreference = 'SilentlyContinue'

  $scriptUrl = 'https://raw.githubusercontent.com/Tobias-Braun/brooom/main/scripts/install.ps1'
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

  # A running brooom.exe cannot be overwritten or deleted on Windows, but it can
  # be renamed. The old binary is moved to brooom.exe.old first (removed on the
  # next run, or right away when nothing holds it) and put back if the copy fails.
  function Install-Binary([string]$source, [string]$target) {
    $old = "$target.old"
    # -LiteralPath everywhere: an install dir containing [ or ] is not a wildcard.
    if (Test-Path -LiteralPath $old) {
      Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
      # A stale .old that is still locked would block the rename below.
      if (Test-Path -LiteralPath $old) {
        throw "Cannot remove the leftover $old (it is still in use); close any process using it, delete it and run the installer again"
      }
    }
    $hadOld = Test-Path -LiteralPath $target
    if ($hadOld) {
      try {
        Move-Item -LiteralPath $target -Destination $old -Force
      } catch {
        throw "Cannot replace $target ($($_.Exception.Message)); close any running brooom and run the installer again"
      }
    }
    try {
      Copy-Item -LiteralPath $source -Destination $target -Force
    } catch {
      if ($hadOld) {
        # A failed copy can leave a partial file; drop it so the old binary can be restored.
        Remove-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
        Move-Item -LiteralPath $old -Destination $target -Force -ErrorAction SilentlyContinue
      }
      throw
    }
    if ($hadOld) { Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue }
  }

  function Get-Sha256([string]$path) {
    try { return (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash } catch { return $null }
  }

  # Returns why br.exe cannot be installed into $dir, or $null when br is free.
  # A br.exe whose hash is in $ownHashes is a hardlink an earlier run created.
  # Get-Command sees PowerShell functions and aliases when the installer runs
  # in the user's session (iex), which is where broot defines its br.
  function Get-BrTakenReason([string]$dir, [string[]]$ownHashes) {
    $br = Join-Path $dir 'br.exe'
    if ((Test-Path -LiteralPath $br) -and ($ownHashes -notcontains (Get-Sha256 $br))) {
      return "$br already exists"
    }
    $other = Get-Command br -ErrorAction SilentlyContinue |
      Where-Object { -not ($_.CommandType -eq 'Application' -and $_.Source -eq $br) } |
      Select-Object -First 1
    if ($other) {
      if ($other.CommandType -eq 'Application') { return "$($other.Source) is already on your PATH" }
      return "br is already a PowerShell $($other.CommandType.ToString().ToLowerInvariant())"
    }
    if (Get-Command broot -ErrorAction SilentlyContinue) { return 'it is used by broot (its br shell function)' }
    return $null
  }

  # Hardlinks br.exe to brooom.exe when br is free and otherwise says why it was
  # skipped. It never fails the install. A hardlink keeps pointing at the binary
  # it was made from, so our br.exe is replaced on every run.
  function Install-Br([string]$dir, [string]$target, [string[]]$ownHashes) {
    $reason = Get-BrTakenReason $dir $ownHashes
    if ($reason) {
      Write-Host "Skipped the br shortcut: $reason. Use brooom instead."
      return
    }
    $br = Join-Path $dir 'br.exe'
    $old = "$br.old"
    try {
      if (Test-Path -LiteralPath $br) {
        # A running br.exe cannot be deleted, but it can be renamed away.
        Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
        Move-Item -LiteralPath $br -Destination $old -Force
        Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
      }
      try {
        New-Item -ItemType HardLink -Path $br -Value $target | Out-Null
      } catch {
        # Some filesystems (FAT, a few network shares) have no hardlinks.
        Copy-Item -LiteralPath $target -Destination $br -Force
      }
      Write-Host "Installed $br (short for brooom)"
    } catch {
      Write-Host "Skipped the br shortcut: cannot create $br ($($_.Exception.Message)). Use brooom instead."
    }
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
    $target = Join-Path $installDir 'brooom.exe'
    # A br.exe hardlinked to the brooom.exe being replaced is still ours.
    $ownHashes = @(if (Test-Path -LiteralPath $target) { Get-Sha256 $target })
    Install-Binary (Join-Path $tmp 'brooom.exe') $target
    Write-Host "Installed $target"
    Install-Br $installDir $target (@($ownHashes) + @(Get-Sha256 $target) | Where-Object { $_ })
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }

  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $onPath = ($userPath -split ';') -contains $installDir
  if (-not $onPath) {
    if ($AddToPath) {
      [Environment]::SetEnvironmentVariable('Path', $(if ($userPath) { "$userPath;$installDir" } else { $installDir }), 'User')
      Write-Host "Added $installDir to your user PATH. Restart your terminal to pick it up."
    } else {
      Write-Host "Warning: $installDir is not in your PATH. Add it with:"
      Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$installDir', 'User')"
      Write-Host "or re-run the installer with the PATH change enabled:"
      Write-Host "  & ([scriptblock]::Create((irm $scriptUrl))) -AddToPath"
      Write-Host "(or set `$env:BROOOM_ADD_TO_PATH = '1' first)."
    }
  }
} $args
