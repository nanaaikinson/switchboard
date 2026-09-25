<#
.SYNOPSIS
Install sb, the Switchboard CLI, from GitHub Releases.

.DESCRIPTION
  irm https://raw.githubusercontent.com/nanaaikinson/switchboard/main/install/install.ps1 | iex
  $env:SB_VERSION = 'v0.1.0'; irm .../install.ps1 | iex    # pin a version
  .\install.ps1 -Global                                      # Program Files; needs an elevated shell

The archive is checked against the release's SHA256SUMS. If minisign is
installed, SHA256SUMS is also checked against its signature. This script only
installs sb.exe and adds its folder to PATH; 'sb setup' makes the system
changes afterwards (after one UAC prompt).

.PARAMETER Global
Install into "$env:ProgramFiles\Switchboard" for every user (default:
"$env:LOCALAPPDATA\Programs\switchboard"). Needs an elevated PowerShell.
Through 'iex', set $env:SB_GLOBAL = '1' instead.

.PARAMETER NoModifyPath
Don't add the install folder to PATH; only print how to.
Through 'iex', set $env:SB_NO_MODIFY_PATH = '1' instead.
#>
[CmdletBinding()]
param(
	[switch]$Global,
	[switch]$NoModifyPath
)

# Public key that signs SHA256SUMS (SHA256SUMS.minisig). Empty until releases
# are signed; the signature check is skipped while it is empty. Keep it the
# same as MINISIGN_PUBKEY in install.sh.
$SbMinisignPubkey = ''
$SbRepo = 'nanaaikinson/switchboard'

function Write-SbInfo {
	[Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingWriteHost', '', Justification = 'Progress for a person; this script returns nothing.')]
	param([string]$Message)
	Write-Host "sb-install: $Message"
}

function Write-SbWarning([string]$Message) {
	Write-Warning "sb-install: $Message"
}

function Get-SbArch {
	# A 32-bit PowerShell on 64-bit Windows reports the real CPU here.
	$arch = $env:PROCESSOR_ARCHITEW6432
	if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
	switch ($arch) {
		'AMD64' { return 'amd64' }
		'ARM64' { return 'arm64' }
		default { throw "unsupported CPU architecture $arch; see https://github.com/$SbRepo/releases" }
	}
}

function Save-SbFile([string]$Url, [string]$OutFile) {
	# -UseBasicParsing: Windows PowerShell 5.1 otherwise needs Internet Explorer.
	Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing -MaximumRedirection 5
}

function Get-SbLatestVersion {
	try {
		$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$SbRepo/releases/latest" -UseBasicParsing
	} catch {
		throw "could not look up the latest release; set `$env:SB_VERSION = 'vX.Y.Z' to pick one"
	}
	if (-not $release.tag_name) { throw "no release found; set `$env:SB_VERSION = 'vX.Y.Z' to pick one" }
	return [string]$release.tag_name
}

function Test-SbAdmin {
	$id = [Security.Principal.WindowsIdentity]::GetCurrent()
	return ([Security.Principal.WindowsPrincipal]$id).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-SbSignature([string]$Base, [string]$Tmp) {
	if (-not (Get-Command minisign -ErrorAction SilentlyContinue)) {
		Write-SbInfo 'minisign not found; skipping signature check (checksum is still verified)'
		return
	}
	if (-not $SbMinisignPubkey) {
		Write-SbInfo 'releases are not signed yet; skipping signature check (checksum is still verified)'
		return
	}
	try {
		Save-SbFile "$Base/SHA256SUMS.minisig" "$Tmp\SHA256SUMS.minisig"
	} catch {
		throw 'could not download SHA256SUMS.minisig; refusing to install unsigned files'
	}
	& minisign -Vqm "$Tmp\SHA256SUMS" -x "$Tmp\SHA256SUMS.minisig" -P $SbMinisignPubkey
	if ($LASTEXITCODE -ne 0) {
		throw 'SHA256SUMS signature is invalid; the release may have been tampered with. Do not install it'
	}
	Write-SbInfo 'verified SHA256SUMS signature'
}

# Test-SbOnPath reports whether dir is one of the entries of path.
function Test-SbOnPath([string]$Path, [string]$Dir) {
	$want = $Dir.TrimEnd('\')
	foreach ($entry in ($Path -split ';')) {
		if ($entry -and ($entry.TrimEnd('\') -ieq $want)) { return $true }
	}
	return $false
}

function Install-Sb([bool]$ForEveryone, [bool]$KeepPath) {
	$ErrorActionPreference = 'Stop'
	$ProgressPreference = 'SilentlyContinue' # the progress bar makes 5.1's downloads crawl
	# Windows PowerShell 5.1 may default to TLS 1.0, which GitHub refuses.
	[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

	$arch = Get-SbArch
	$version = $env:SB_VERSION
	if (-not $version) { $version = Get-SbLatestVersion }
	if (-not $version.StartsWith('v')) { $version = "v$version" }
	if ($version -notmatch '^v[0-9][0-9A-Za-z.+-]*$') {
		throw "SB_VERSION=$($env:SB_VERSION) is not a release tag like v0.1.0"
	}

	if ($ForEveryone) {
		if (-not (Test-SbAdmin)) { throw '-Global installs into Program Files; run this from an elevated PowerShell (Run as administrator)' }
		$dir = Join-Path $env:ProgramFiles 'Switchboard'
		$scope = 'Machine'
	} else {
		if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set; use -Global' }
		$dir = Join-Path $env:LOCALAPPDATA 'Programs\switchboard'
		$scope = 'User'
	}

	$name = "sb_$($version.Substring(1))_windows_$arch"
	$asset = "$name.zip"
	$base = "https://github.com/$SbRepo/releases/download/$version"
	$tmp = Join-Path ([IO.Path]::GetTempPath()) ("sb-install-" + [Guid]::NewGuid().ToString('N'))
	New-Item -ItemType Directory -Path $tmp | Out-Null
	try {
		Write-SbInfo "downloading sb $version for windows/$arch"
		try {
			Save-SbFile "$base/$asset" "$tmp\$asset"
		} catch {
			throw "could not download $asset; check that release $version exists at https://github.com/$SbRepo/releases"
		}
		try {
			Save-SbFile "$base/SHA256SUMS" "$tmp\SHA256SUMS"
		} catch {
			throw "could not download SHA256SUMS for $version; refusing to install unverified files"
		}

		Test-SbSignature $base $tmp

		$want = $null
		foreach ($line in Get-Content -LiteralPath "$tmp\SHA256SUMS") {
			$fields = $line -split '\s+', 2
			if ($fields.Count -eq 2 -and ($fields[1] -eq $asset -or $fields[1] -eq "*$asset")) { $want = $fields[0].ToLowerInvariant(); break }
		}
		if (-not $want) { throw "$asset is not listed in SHA256SUMS; refusing to install unverified files" }
		$got = (Get-FileHash -LiteralPath "$tmp\$asset" -Algorithm SHA256).Hash.ToLowerInvariant()
		if ($got -ne $want) { throw "checksum mismatch for $asset (got $got, want $want); the download is corrupt or was tampered with" }
		Write-SbInfo 'verified checksum'

		Expand-Archive -LiteralPath "$tmp\$asset" -DestinationPath $tmp -Force
		$src = Join-Path $tmp "$name\sb.exe"
		if (-not (Test-Path -LiteralPath $src -PathType Leaf)) { throw "$asset does not contain $name\sb.exe" }

		New-Item -ItemType Directory -Path $dir -Force | Out-Null
		$dest = Join-Path $dir 'sb.exe'
		$staged = Join-Path $dir '.sb.tmp.exe'
		Copy-Item -LiteralPath $src -Destination $staged -Force
		# A running sb.exe (the daemon) can't be overwritten, but it can be
		# renamed; sb.old.exe is also what 'sb rollback' goes back to.
		if (Test-Path -LiteralPath $dest) {
			$old = Join-Path $dir 'sb.old.exe'
			Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
			if (Test-Path -LiteralPath $old) { throw "can't replace $old; it may be running. Stop the daemon (sb uninstall, or Task Manager) and re-run" }
			Move-Item -LiteralPath $dest -Destination $old
		}
		Move-Item -LiteralPath $staged -Destination $dest
		Write-SbInfo "installed sb $version to $dest"
	} finally {
		Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
	}

	$sb = 'sb'
	$stored = [string][Environment]::GetEnvironmentVariable('Path', $scope)
	if (-not (Test-SbOnPath $stored $dir)) {
		if ($KeepPath) {
			$sb = $dest
			Write-SbWarning "$dir is not on your PATH. To add it:"
			Write-SbWarning "    [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', '$scope') + ';$dir', '$scope')"
		} else {
			$new = if ($stored) { $stored.TrimEnd(';') + ";$dir" } else { $dir }
			[Environment]::SetEnvironmentVariable('Path', $new, $scope)
			Write-SbInfo "added $dir to your $($scope.ToLowerInvariant()) PATH; new terminals will find sb"
		}
	}
	# This session too, so the next step works without a new terminal.
	$session = [string]$env:Path
	if (-not (Test-SbOnPath $session $dir)) { $env:Path = $session.TrimEnd(';') + ";$dir" }

	Write-SbInfo "next: run '$sb setup' to finish (Windows asks for administrator permission once). Re-run it after every upgrade."
}

$sbGlobal = $Global.IsPresent -or $env:SB_GLOBAL -eq '1'
$sbKeepPath = $NoModifyPath.IsPresent -or $env:SB_NO_MODIFY_PATH -eq '1'
try {
	Install-Sb $sbGlobal $sbKeepPath
} catch {
	Write-Error -Message "sb-install: error: $($_.Exception.Message)" -ErrorAction Continue
	# 'exit' would close the window of someone who ran 'irm ... | iex'.
	if ($MyInvocation.MyCommand.Path) { exit 1 }
}
