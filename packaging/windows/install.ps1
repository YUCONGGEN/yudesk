param(
    [string]$SourceDir = (Join-Path $PSScriptRoot '..\..\dist\windows-amd64'),
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\YuDesk'),
    [Parameter(Mandatory = $true)][string]$Relay,
    [Parameter(Mandatory = $true)][string]$RelayFingerprint,
    [string]$ShareDir = '',
    [string]$UpdateManifest = '',
    [string]$UpdatePublicKey = ''
)

$ErrorActionPreference = 'Stop'
$resolvedSource = (Resolve-Path -LiteralPath $SourceDir).Path
$required = @('yudesk-agent.exe', 'yudesk-viewer.exe', 'yudesk-account.exe', 'yudesk-update.exe')
foreach ($name in $required) {
    if (-not (Test-Path -LiteralPath (Join-Path $resolvedSource $name))) {
        throw "Missing $name in $resolvedSource"
    }
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
foreach ($name in $required) {
    Copy-Item -LiteralPath (Join-Path $resolvedSource $name) -Destination (Join-Path $InstallDir $name) -Force
}
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'update.ps1') -Destination (Join-Path $InstallDir 'update.ps1') -Force

$agent = Join-Path $InstallDir 'yudesk-agent.exe'
$agentCommand = '"' + $agent + '" -relay "' + $Relay + '" -relay-fingerprint "' + $RelayFingerprint + '"'
if ($ShareDir) {
    $resolvedShare = (Resolve-Path -LiteralPath $ShareDir).Path
    $agentCommand += ' -share-dir "' + $resolvedShare + '"'
}
New-Item -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Force | Out-Null
New-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name 'YuDeskAgent' -Value $agentCommand -PropertyType String -Force | Out-Null
$agentArguments = @('-relay', $Relay, '-relay-fingerprint', $RelayFingerprint)
if ($ShareDir) { $agentArguments += @('-share-dir', $resolvedShare) }
$sessionFile = Join-Path $env:APPDATA 'yudesk\session-token'
if (Test-Path -LiteralPath $sessionFile) {
    Start-Process -FilePath $agent -ArgumentList $agentArguments -WindowStyle Hidden
} else {
    Write-Host 'Run yudesk-account.exe login and activate before starting the agent.'
}
if ($UpdateManifest -and $UpdatePublicKey) {
    $updater = Join-Path $InstallDir 'yudesk-update.exe'
    $updateScript = Join-Path $InstallDir 'update.ps1'
    $updateArguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $updateScript + '" -Manifest "' + $UpdateManifest + '" -PublicKey "' + $UpdatePublicKey + '" -AgentPath "' + $agent + '" -UpdaterPath "' + $updater + '" -Relay "' + $Relay + '" -RelayFingerprint "' + $RelayFingerprint + '"'
    if ($ShareDir) { $updateArguments += ' -ShareDir "' + $resolvedShare + '"' }
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument $updateArguments
    $trigger = New-ScheduledTaskTrigger -Daily -At '03:00'
    Register-ScheduledTask -TaskName 'YuDesk Update' -Action $action -Trigger $trigger -Description 'Install Ed25519-signed YuDesk agent updates' -Force | Out-Null
    Write-Host 'Daily signed updates configured for 03:00.'
}
Write-Host "YuDesk installed in $InstallDir and configured for current-user startup."
