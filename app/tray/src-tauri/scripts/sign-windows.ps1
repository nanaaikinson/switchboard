# Signs a Windows binary for `tauri build` (bundle.windows.signCommand).
# Credentials come only from the environment; nothing is stored in the repo:
#   WINDOWS_CERTIFICATE           base64 of a .pfx code-signing certificate
#   WINDOWS_CERTIFICATE_PASSWORD  its password
#   WINDOWS_TIMESTAMP_URL         optional, default http://timestamp.digicert.com
# Without WINDOWS_CERTIFICATE the file is left unsigned (local builds).
param([Parameter(Mandatory = $true)][string]$File)
$ErrorActionPreference = "Stop"

if (-not $env:WINDOWS_CERTIFICATE) {
  Write-Host "sign-windows: WINDOWS_CERTIFICATE not set; leaving $File unsigned"
  exit 0
}
$pfx = Join-Path ([IO.Path]::GetTempPath()) ("sb-sign-" + [guid]::NewGuid() + ".pfx")
try {
  [IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:WINDOWS_CERTIFICATE))
  $ts = if ($env:WINDOWS_TIMESTAMP_URL) { $env:WINDOWS_TIMESTAMP_URL } else { "http://timestamp.digicert.com" }
  $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" | Sort-Object FullName -Descending | Select-Object -First 1
  if (-not $signtool) { throw "signtool.exe not found; install the Windows SDK" }
  & $signtool.FullName sign /f $pfx /p $env:WINDOWS_CERTIFICATE_PASSWORD /fd sha256 /tr $ts /td sha256 $File
  if ($LASTEXITCODE -ne 0) { throw "signtool failed with exit code $LASTEXITCODE" }
} finally {
  Remove-Item -Force -ErrorAction SilentlyContinue $pfx
}
