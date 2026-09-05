param(
    [ValidateSet('all', 'windows', 'linux', 'darwin')]
    [string]$Target = 'all'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$components = @('yudesk-agent', 'yudesk-viewer')
$obsoleteComponents = @('yudesk-account', 'yudesk-admin', 'yudesk-relay', 'yudesk-update')
$targets = @(
    @{ OS = 'windows'; Arch = 'amd64' },
    @{ OS = 'linux'; Arch = 'amd64' },
    @{ OS = 'darwin'; Arch = 'amd64' },
    @{ OS = 'darwin'; Arch = 'arm64' }
)
if ($Target -ne 'all') {
    $targets = $targets | Where-Object { $_.OS -eq $Target }
}

$localGo = Get-Command go -ErrorAction SilentlyContinue
if (-not $localGo -and -not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Go 1.22+ or Docker is required.'
}

Push-Location $projectRoot
try {
    foreach ($buildTarget in $targets) {
        $outputDir = Join-Path $projectRoot "dist/$($buildTarget.OS)-$($buildTarget.Arch)"
        New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
        $obsoleteSuffix = if ($buildTarget.OS -eq 'windows') { '.exe' } else { '' }
        foreach ($obsolete in $obsoleteComponents) {
            $obsoletePath = Join-Path $outputDir ($obsolete + $obsoleteSuffix)
            if (Test-Path -LiteralPath $obsoletePath -PathType Leaf) {
                Remove-Item -LiteralPath $obsoletePath -Force
            }
        }
        foreach ($component in $components) {
            $suffix = if ($buildTarget.OS -eq 'windows') { '.exe' } else { '' }
            $output = Join-Path $outputDir ($component + $suffix)
            $linkerFlags = if ($buildTarget.OS -eq 'windows') { '-s -w -H=windowsgui' } else { '-s -w' }
            if ($localGo) {
                $previousGOOS, $previousGOARCH = $env:GOOS, $env:GOARCH
                $env:GOOS, $env:GOARCH = $buildTarget.OS, $buildTarget.Arch
                try {
                    & go build -trimpath -ldflags $linkerFlags -o $output "./cmd/$component"
                    if ($LASTEXITCODE -ne 0) { throw "build failed: $component" }
                } finally {
                    $env:GOOS, $env:GOARCH = $previousGOOS, $previousGOARCH
                }
            } else {
                $containerOutput = "/src/dist/$($buildTarget.OS)-$($buildTarget.Arch)/$component$suffix"
                & docker run --rm -e "GOOS=$($buildTarget.OS)" -e "GOARCH=$($buildTarget.Arch)" -v "${projectRoot}:/src" -w /src -v yudesk-gomod:/go/pkg/mod -v yudesk-gocache:/root/.cache/go-build golang:1.22 sh -lc "/usr/local/go/bin/go build -trimpath -ldflags='$linkerFlags' -o '$containerOutput' './cmd/$component'"
                if ($LASTEXITCODE -ne 0) { throw "build failed: $component" }
            }
        }
    }
    if ($Target -eq 'all' -or $Target -eq 'darwin') {
        $serverOutputDir = Join-Path $projectRoot 'dist/server'
        New-Item -ItemType Directory -Force -Path $serverOutputDir | Out-Null
        $serverOutput = Join-Path $serverOutputDir 'yudesk-relay'
        if ($localGo) {
            $previousGOOS, $previousGOARCH = $env:GOOS, $env:GOARCH
            $env:GOOS, $env:GOARCH = 'darwin', 'arm64'
            try {
                & go build -trimpath -ldflags '-s -w' -o $serverOutput './cmd/yudesk-relay'
                if ($LASTEXITCODE -ne 0) { throw 'server build failed' }
            } finally {
                $env:GOOS, $env:GOARCH = $previousGOOS, $previousGOARCH
            }
        } else {
            & docker run --rm -e GOOS=darwin -e GOARCH=arm64 -v "${projectRoot}:/src" -w /src -v yudesk-gomod:/go/pkg/mod -v yudesk-gocache:/root/.cache/go-build golang:1.22 sh -lc "/usr/local/go/bin/go build -trimpath -ldflags='-s -w' -o '/src/dist/server/yudesk-relay' './cmd/yudesk-relay'"
            if ($LASTEXITCODE -ne 0) { throw 'server build failed' }
        }
    }
    $distRoot = Join-Path $projectRoot 'dist'
    foreach ($legacyName in @(
        'yudesk-account-darwin-amd64', 'yudesk-account-linux-amd64', 'yudesk-account-windows-amd64.exe',
        'yudesk-agent-darwin-amd64', 'yudesk-agent-linux-amd64', 'yudesk-agent-windows-amd64.exe',
        'yudesk-relay-darwin-amd64', 'yudesk-relay-linux-amd64', 'yudesk-relay-windows-amd64.exe',
        'yudesk-viewer-darwin-amd64', 'yudesk-viewer-linux-amd64', 'yudesk-viewer-windows-amd64.exe'
    )) {
        $legacyPath = Join-Path $distRoot $legacyName
        if (Test-Path -LiteralPath $legacyPath -PathType Leaf) {
            Remove-Item -LiteralPath $legacyPath -Force
        }
    }
    $hashes = Get-ChildItem -LiteralPath $distRoot -Recurse -File |
        Where-Object { $_.Directory.Name -in @('windows-amd64', 'linux-amd64', 'darwin-amd64', 'darwin-arm64') -and $_.BaseName -in $components } |
        Sort-Object FullName |
        ForEach-Object {
            $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
            $relative = $_.FullName.Substring($distRoot.Length + 1).Replace('\', '/')
            "$hash  $relative"
        }
    $checksumPath = Join-Path $projectRoot 'dist/SHA256SUMS.txt'
    [System.IO.File]::WriteAllText($checksumPath, (($hashes -join "`n") + "`n"), [System.Text.Encoding]::ASCII)
    Write-Host "Build complete: $(Join-Path $projectRoot 'dist')"
} finally {
    Pop-Location
}
