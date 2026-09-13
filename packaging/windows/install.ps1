param(
    [string]$SourceDir = (Join-Path $PSScriptRoot '..\..\dist\windows-amd64'),
    [string]$InstallDir = (Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'YuDesk'),
    [switch]$Verify
)

$ErrorActionPreference = 'Stop'

function Resolve-LocalInstallDirectory([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path) -or $Path.Contains('"') -or -not [IO.Path]::IsPathRooted($Path.Trim())) {
        throw 'Select an absolute installation directory on a local disk.'
    }
    $full = [IO.Path]::GetFullPath($Path.Trim())
    if ($full.StartsWith('\\') -or [string]::IsNullOrWhiteSpace([IO.Path]::GetPathRoot($full))) {
        throw 'The installation directory cannot be a network path.'
    }
    if ($full.TrimEnd('\') -eq [IO.Path]::GetPathRoot($full).TrimEnd('\')) {
        throw 'The installation directory cannot be a disk root.'
    }
    return $full.TrimEnd('\')
}

$resolvedSource = (Resolve-Path -LiteralPath $SourceDir).Path
$source = Join-Path $resolvedSource 'yudesk.exe'
if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "The package is missing the unified yudesk.exe: $resolvedSource"
}
$resolvedInstall = Resolve-LocalInstallDirectory $InstallDir
$installed = Join-Path $resolvedInstall 'yudesk.exe'

if ($Verify) {
    [PSCustomObject]@{
        SourcePath = $source
        InstallDir = $resolvedInstall
        InstalledPath = $installed
        Mode = 'UnifiedDesktop'
    }
    return
}

# The unified executable performs the elevated, transaction-safe copy, service
# setup and Programs and Features registration. There is no updater/legacy
# account process and only one UAC child.
$argument = '-desktop-setup-install "' + $resolvedInstall + '"'
$process = Start-Process -FilePath $source -ArgumentList $argument -Verb RunAs -WindowStyle Hidden -Wait -PassThru
if ($process.ExitCode -ne 0) {
    throw 'YuDesk installation did not complete. Exit the old YuDesk process and retry.'
}
if (-not (Test-Path -LiteralPath $installed -PathType Leaf)) {
    throw "The installed executable is missing: $installed"
}
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $source).Hash -ne (Get-FileHash -Algorithm SHA256 -LiteralPath $installed).Hash) {
    throw 'The installed YuDesk executable failed integrity verification.'
}

$registration = Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\YuDesk' -ErrorAction Stop
if ($registration.DisplayName -ne 'YuDesk' -or
    [IO.Path]::GetFullPath([string]$registration.InstallLocation) -ne $resolvedInstall -or
    [string]$registration.UninstallString -notmatch '-desktop-uninstall$') {
    throw 'YuDesk was not registered in Windows Programs and Features.'
}

# User shortcuts are created after the elevated transaction. Shortcut failures
# do not invalidate a healthy service/control-panel installation.
try {
    $shell = New-Object -ComObject WScript.Shell
    $programs = Join-Path ([Environment]::GetFolderPath('Programs')) 'YuDesk.lnk'
    $desktop = Join-Path ([Environment]::GetFolderPath('Desktop')) 'YuDesk.lnk'
    foreach ($link in @($programs, $desktop)) {
        if ([string]::IsNullOrWhiteSpace($link)) { continue }
        $shortcut = $shell.CreateShortcut($link)
        $shortcut.TargetPath = $installed
        $shortcut.WorkingDirectory = $resolvedInstall
        $shortcut.IconLocation = "$installed,0"
        $shortcut.Description = 'YuDesk low-latency remote desktop'
        $shortcut.Save()
    }
} catch {
    Write-Warning "YuDesk is installed, but shortcuts could not be created: $($_.Exception.Message)"
}

# Single-instance handling in YuDesk restores an existing tray process; on a
# cold start this creates exactly one GUI process.
Start-Process -FilePath $installed -WorkingDirectory $resolvedInstall | Out-Null
Write-Host "YuDesk was installed in $resolvedInstall and can be removed from Programs and Features."
