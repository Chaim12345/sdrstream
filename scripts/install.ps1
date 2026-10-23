# One-command install for sdrstream on Windows (PowerShell 5.1+).
#
#   irm https://raw.githubusercontent.com/Chaim12345/sdrstream/master/scripts/install.ps1 | iex
#
# Installs the static Windows binary, sets a first-run UI password without
# echoing it, stores it in a user-only config file, and registers a
# Scheduled Task that starts the monitor at logon.
$ErrorActionPreference = 'Stop'

$Repo    = 'Chaim12345/sdrstream'
$Version = if ($env:SDRSTREAM_VERSION) { $env:SDRSTREAM_VERSION } else { 'latest' }
$Port    = if ($env:SDRSTREAM_PORT) { $env:SDRSTREAM_PORT } else { 8000 }
$Root    = Join-Path $env:LOCALAPPDATA 'sdrstream'
$BinDir  = Join-Path $Root 'bin'
$State   = Join-Path $Root 'state'
$Conf    = Join-Path $Root 'sdrstream.env'
New-Item -ItemType Directory -Force $BinDir, $State | Out-Null

$exe = Join-Path $BinDir 'sdrstream.exe'
if ($Version -eq 'latest') {
  $url = "https://github.com/$Repo/releases/latest/download/sdrstream_windows_amd64.exe"
} else {
  $url = "https://github.com/$Repo/releases/download/$Version/sdrstream_${Version}_windows_amd64.exe"
}
Write-Host "==> downloading $url"
try {
  Invoke-WebRequest -Uri $url -OutFile $exe -UseBasicParsing
} catch {
  Write-Host 'No published release for this platform yet; build from source instead:'
  Write-Host '  git clone https://github.com/Chaim12345/sdrstream; cd sdrstream\go; go build -o sdrstream.exe ./cmd/sdrstream'
  exit 1
}

if (-not (Test-Path $Conf)) {
  $secure = Read-Host -AsSecureString 'Choose a password for the sdrstream web UI'
  $pw = [Runtime.InteropServices.Marshal]::PtrToStringBSTR(
          [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure))
  $acl = Get-Acl $Root
  $acl.SetAccessRuleProtection($true, $false)  # disable inheritance: user-only
  $rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
            $env:USERNAME, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
  $acl.SetAccessRule($rule)
  Set-Acl $Root $acl
  Set-Content -Path $Conf -NoNewline -Value "SDRSTREAM_UI_PASSWORD=$pw`nSDRSTREAM_STATE_DIR=$State"
  $pw = $null
  Write-Host 'password stored (never printed) in' $Conf
}

$action = New-ScheduledTaskAction -Execute $exe -Argument "web --host 127.0.0.1 --port $Port"
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
            -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName 'sdrstream' -Action $action -Trigger $trigger `
  -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName 'sdrstream'

Write-Host ''
Write-Host "==> done. Open http://127.0.0.1:$Port and log in."
Write-Host "    Password file (user-only ACL): $Conf"
