$ErrorActionPreference = 'Stop'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('yudesk-install-test-' + [Guid]::NewGuid().ToString('N'))
try {
    $source = Join-Path $testRoot 'source'
    $target = Join-Path $testRoot 'custom install'
    New-Item -ItemType Directory -Force -Path $source | Out-Null
    Set-Content -LiteralPath (Join-Path $source 'yudesk.exe') -Value 'fixture' -NoNewline

    $plan = & (Join-Path $PSScriptRoot 'install.ps1') -SourceDir $source -InstallDir $target -Verify
    if ($plan.Mode -ne 'UnifiedDesktop') { throw 'installer did not select unified desktop mode' }
    if ($plan.InstallDir -ne [IO.Path]::GetFullPath($target)) { throw 'custom installation directory was not preserved' }
    if ($plan.InstalledPath -ne (Join-Path ([IO.Path]::GetFullPath($target)) 'yudesk.exe')) { throw 'installed executable path is incorrect' }

    $text = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'install.ps1') -Raw
    foreach ($legacy in @('yudesk-agent.exe', 'yudesk-viewer.exe', 'yudesk-account.exe', 'yudesk-update.exe')) {
        if ($text.Contains($legacy)) { throw "legacy component remains required: $legacy" }
    }
    foreach ($required in @('-desktop-setup-install', 'CurrentVersion\Uninstall\YuDesk', 'YuDesk.lnk')) {
        if (-not $text.Contains($required)) { throw "installer contract missing: $required" }
    }

    $invalidAccepted = $false
    try {
        & (Join-Path $PSScriptRoot 'install.ps1') -SourceDir $source -InstallDir ([IO.Path]::GetPathRoot($target)) -Verify | Out-Null
        $invalidAccepted = $true
    } catch {}
    if ($invalidAccepted) { throw 'installer accepted a disk root as the target' }
    Write-Output 'PASS: unified custom-directory installation plan and uninstall registration contract'
} finally {
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
