# Tests install/install.ps1 against a fake release, with downloads served from
# a local folder. Runs on Windows PowerShell 5.1 and pwsh 7 (Windows, Linux).
# It only writes under a temporary folder and never changes PATH.
#
#   pwsh -NoProfile -File test/install/install-test.ps1
$ErrorActionPreference = 'Stop'
$script = Join-Path $PSScriptRoot '../../install/install.ps1'

# Load install.ps1's functions and settings without running its install.
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile((Resolve-Path $script), [ref]$tokens, [ref]$errors)
if ($errors) { throw "install.ps1 doesn't parse: $errors" }
foreach ($f in $ast.FindAll({ param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] }, $false)) {
	. ([scriptblock]::Create($f.Extent.Text))
}
$SbRepo = 'nanaaikinson/switchboard'
$script:SbMinisignPubkey = '' # no release key, as now; the signature tests pin one

$root = Join-Path ([IO.Path]::GetTempPath()) ('sb-install-test-' + [Guid]::NewGuid().ToString('N'))
$release = Join-Path $root 'release'
New-Item -ItemType Directory -Path $release | Out-Null
$savedEnv = @{ SB_VERSION = $env:SB_VERSION; LOCALAPPDATA = $env:LOCALAPPDATA; PROCESSOR_ARCHITECTURE = $env:PROCESSOR_ARCHITECTURE; PROCESSOR_ARCHITEW6432 = $env:PROCESSOR_ARCHITEW6432
	SB_INSECURE_SKIP_SIGNATURE = $env:SB_INSECURE_SKIP_SIGNATURE; FAKE_COMMENT = $env:FAKE_COMMENT; FAKE_BAD_SIGNATURE = $env:FAKE_BAD_SIGNATURE }
$failures = 0

# Save-SbFile serves the fake release instead of GitHub.
function Save-SbFile([string]$Url, [string]$OutFile) {
	if (-not $Url.StartsWith("https://github.com/$SbRepo/releases/download/")) { throw "unexpected URL $Url" }
	$src = Join-Path $release ($Url -split '/')[-1]
	if (-not (Test-Path -LiteralPath $src)) { throw "404 $Url" }
	Copy-Item -LiteralPath $src -Destination $OutFile
}

# A stand-in minisign: it prints $env:FAKE_COMMENT as the trusted comment, or
# fails when $env:FAKE_BAD_SIGNATURE is 1. Find-SbMinisign finds it only while
# $useFakeMinisign is set, so a real minisign on this machine is never used.
$onWindows = $env:OS -eq 'Windows_NT'
if ($onWindows) {
	$fakeMinisign = Join-Path $root 'minisign.cmd'
	Set-Content -LiteralPath $fakeMinisign -Value "@echo off`r`nif `"%FAKE_BAD_SIGNATURE%`"==`"1`" exit /b 1`r`necho %FAKE_COMMENT%`r`n"
} else {
	$fakeMinisign = Join-Path $root 'minisign'
	Set-Content -LiteralPath $fakeMinisign -Value "#!/bin/sh`n[ `"`$FAKE_BAD_SIGNATURE`" != 1 ] || exit 1`nprintf '%s\n' `"`$FAKE_COMMENT`"`n"
	& chmod +x $fakeMinisign
}
$script:useFakeMinisign = $false
function Find-SbMinisign {
	if ($script:useFakeMinisign) { return Get-Command $fakeMinisign }
}

function Build-FakeRelease([string]$Version, [string]$Content) {
	Remove-Item -Path (Join-Path $release '*') -Recurse -Force
	$name = "sb_$($Version.TrimStart('v'))_windows_amd64"
	$stage = Join-Path $root "stage-$Version"
	New-Item -ItemType Directory -Path (Join-Path $stage $name) -Force | Out-Null
	Set-Content -LiteralPath (Join-Path $stage "$name/sb.exe") -Value $Content -NoNewline
	Compress-Archive -Path (Join-Path $stage $name) -DestinationPath (Join-Path $release "$name.zip") -Force
	$hash = (Get-FileHash -LiteralPath (Join-Path $release "$name.zip") -Algorithm SHA256).Hash.ToLowerInvariant()
	Set-Content -LiteralPath (Join-Path $release 'SHA256SUMS') -Value "0000  sb_other_linux_amd64.tar.gz`n$hash  $name.zip`n"
	Set-Content -LiteralPath (Join-Path $release 'SHA256SUMS.minisig') -Value 'untrusted comment: fake'
	return $name
}

function Test-Case([string]$Name, [scriptblock]$Body) {
	try {
		& $Body
		Write-Output "ok   $Name"
	} catch {
		Write-Output "FAIL $Name`: $($_.Exception.Message)"
		$script:failures++
	}
}

function Assert-Failure([scriptblock]$Body, [string]$Like) {
	try { & $Body } catch {
		if ($_.Exception.Message -notlike "*$Like*") { throw "threw '$($_.Exception.Message)', want *$Like*" }
		return
	}
	throw "didn't throw (want *$Like*)"
}

try {
	$env:LOCALAPPDATA = Join-Path $root 'appdata'
	$env:PROCESSOR_ARCHITECTURE = 'AMD64'
	$env:PROCESSOR_ARCHITEW6432 = $null
	$dest = Join-Path $env:LOCALAPPDATA 'Programs/switchboard/sb.exe'
	$old = Join-Path $env:LOCALAPPDATA 'Programs/switchboard/sb.old.exe'

	Test-Case 'installs a verified release' {
		Build-FakeRelease 'v1.2.3' 'one' | Out-Null
		$env:SB_VERSION = '1.2.3'
		$warnings = Install-Sb $false $true 6>$null 3>&1 | Out-String
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'one') { throw 'wrong sb.exe installed' }
		if (Test-Path -LiteralPath (Join-Path (Split-Path $dest) '.sb.tmp.exe')) { throw 'staged file left behind' }
		if ($warnings -notlike '*this release is NOT signature-verified*') { throw "no warning that the release isn't signature-verified: $warnings" }
	}

	Test-Case 'upgrading keeps the previous sb as sb.old.exe' {
		Build-FakeRelease 'v1.3.0' 'two' | Out-Null
		$env:SB_VERSION = 'v1.3.0'
		Install-Sb $false $true 6>$null 3>$null
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'two') { throw 'not upgraded' }
		if ((Get-Content -LiteralPath $old -Raw) -ne 'one') { throw 'sb.old.exe is not the previous sb' }
	}

	Test-Case 'refuses a checksum mismatch and leaves sb alone' {
		$name = Build-FakeRelease 'v1.4.0' 'evil'
		Set-Content -LiteralPath (Join-Path $release 'SHA256SUMS') -Value "$('ab' * 32)  $name.zip"
		$env:SB_VERSION = 'v1.4.0'
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'checksum mismatch'
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'two') { throw 'sb.exe changed' }
	}

	Test-Case 'refuses an archive missing from SHA256SUMS' {
		Build-FakeRelease 'v1.5.0' 'x' | Out-Null
		Set-Content -LiteralPath (Join-Path $release 'SHA256SUMS') -Value "0000  sb_1.5.0_windows_arm64.zip"
		$env:SB_VERSION = 'v1.5.0'
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'not listed in SHA256SUMS'
	}

	Test-Case 'reports a missing release' {
		Remove-Item -Path (Join-Path $release '*') -Force
		$env:SB_VERSION = 'v9.9.9'
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'check that release v9.9.9 exists'
	}

	Test-Case 'rejects versions that are not tags' {
		foreach ($v in @('v1.0;calc', 'latest', 'v', 'v1 0')) {
			$env:SB_VERSION = $v
			Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'is not a release tag'
		}
	}

	# With the release key pinned, as install.ps1 will be once it exists.
	$script:SbMinisignPubkey = 'RWfake-release-key'
	$env:FAKE_COMMENT = 'switchboard v1.6.0 SHA256SUMS'

	Test-Case 'pinned key, no minisign: refuses and says how to get it' {
		Build-FakeRelease 'v1.6.0' 'six' | Out-Null
		$env:SB_VERSION = 'v1.6.0'
		$script:useFakeMinisign = $false
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'minisign is needed to verify the release signature. Install it (scoop install minisign'
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'two') { throw 'sb.exe changed' }
	}

	Test-Case 'pinned key, no minisign, opted out: installs with a warning' {
		$script:useFakeMinisign = $false
		$env:SB_INSECURE_SKIP_SIGNATURE = '1'
		$warnings = Install-Sb $false $true 6>$null 3>&1 | Out-String
		$env:SB_INSECURE_SKIP_SIGNATURE = $null
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'six') { throw 'not installed' }
		if ($warnings -notlike '*SB_INSECURE_SKIP_SIGNATURE=1: NOT checking the release signature*') { throw "no warning: $warnings" }
	}

	Test-Case 'pinned key, valid signature: installs' {
		Build-FakeRelease 'v1.6.0' 'six-signed' | Out-Null
		$script:useFakeMinisign = $true
		Install-Sb $false $true 6>$null 3>$null
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'six-signed') { throw 'not installed' }
	}

	Test-Case 'pinned key, invalid signature: refuses, even when opted out' {
		Build-FakeRelease 'v1.6.0' 'evil' | Out-Null
		$script:useFakeMinisign = $true
		$env:FAKE_BAD_SIGNATURE = '1'
		$env:SB_INSECURE_SKIP_SIGNATURE = '1'
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'SHA256SUMS signature is invalid'
		$env:FAKE_BAD_SIGNATURE = $null
		$env:SB_INSECURE_SKIP_SIGNATURE = $null
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'six-signed') { throw 'sb.exe changed' }
	}

	Test-Case "pinned key, another release's signature: refuses" {
		$script:useFakeMinisign = $true
		$env:FAKE_COMMENT = 'switchboard v1.0.0 SHA256SUMS'
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } "signed as 'switchboard v1.0.0 SHA256SUMS', not 'switchboard v1.6.0 SHA256SUMS'"
		$env:FAKE_COMMENT = 'switchboard v1.6.0 SHA256SUMS'
	}

	Test-Case 'pinned key, unsigned release: refuses' {
		$script:useFakeMinisign = $true
		Remove-Item -LiteralPath (Join-Path $release 'SHA256SUMS.minisig')
		Assert-Failure { Install-Sb $false $true 6>$null 3>$null } 'could not download SHA256SUMS.minisig'
		if ((Get-Content -LiteralPath $dest -Raw) -ne 'six-signed') { throw 'sb.exe changed' }
	}
	$script:SbMinisignPubkey = ''
	$script:useFakeMinisign = $false

	Test-Case 'detects the CPU' {
		$env:PROCESSOR_ARCHITECTURE = 'ARM64'
		if ((Get-SbArch) -ne 'arm64') { throw 'ARM64' }
		$env:PROCESSOR_ARCHITECTURE = 'x86'; $env:PROCESSOR_ARCHITEW6432 = 'AMD64'
		if ((Get-SbArch) -ne 'amd64') { throw '32-bit shell on 64-bit Windows' }
		$env:PROCESSOR_ARCHITEW6432 = $null
		Assert-Failure { Get-SbArch } 'unsupported CPU architecture x86'
		$env:PROCESSOR_ARCHITECTURE = 'AMD64'
	}

	Test-Case 'matches PATH entries' {
		if (-not (Test-SbOnPath 'C:\Windows;C:\Users\a\AppData\Local\Programs\switchboard\;' 'C:\Users\A\AppData\Local\Programs\switchboard')) { throw 'case or trailing slash' }
		if (Test-SbOnPath 'C:\Windows;C:\switchboard-old' 'C:\switchboard') { throw 'prefix matched' }
		if (Test-SbOnPath '' 'C:\switchboard') { throw 'empty PATH' }
	}
} finally {
	foreach ($k in $savedEnv.Keys) { Set-Item -Path "env:$k" -Value $savedEnv[$k] }
	Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
}

if ($failures) { Write-Output "$failures failed"; exit 1 }
Write-Output 'all passed'
