param(
    [Parameter(Mandatory = $true)][string]$Manifest,
    [Parameter(Mandatory = $true)][string]$PublicKey,
    [Parameter(Mandatory = $true)][string]$AgentPath,
    [Parameter(Mandatory = $true)][string]$UpdaterPath,
    [Parameter(Mandatory = $true)][string]$Relay,
    [Parameter(Mandatory = $true)][string]$RelayFingerprint,
    [string]$ShareDir = ''
)

$ErrorActionPreference = 'Stop'
$resolvedAgent = [IO.Path]::GetFullPath($AgentPath)
$resolvedUpdater = [IO.Path]::GetFullPath($UpdaterPath)
if (-not (Test-Path -LiteralPath $resolvedUpdater)) { throw "Updater not found: $resolvedUpdater" }

$running = Get-CimInstance Win32_Process -Filter "Name='yudesk-agent.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath) -eq $resolvedAgent }
$wasRunning = @($running).Count -gt 0
foreach ($process in $running) {
    Stop-Process -Id $process.ProcessId -Force
}

try {
    & $resolvedUpdater -manifest $Manifest -public-key $PublicKey -component yudesk-agent -destination $resolvedAgent
    if ($LASTEXITCODE -ne 0) { throw 'YuDesk signed update failed' }
} finally {
    if ($wasRunning -and (Test-Path -LiteralPath $resolvedAgent)) {
        $agentArguments = @('-relay', $Relay, '-relay-fingerprint', $RelayFingerprint)
        if ($ShareDir) { $agentArguments += @('-share-dir', $ShareDir) }
        Start-Process -FilePath $resolvedAgent -ArgumentList $agentArguments -WindowStyle Hidden
    }
}
