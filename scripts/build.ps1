param(
    [ValidateSet('all', 'windows', 'linux', 'darwin')]
    [string]$Target = 'all',
    [ValidatePattern('^\d+\.\d+\.\d+$')]
    [string]$GoVersion = '1.27.1'
)

$ErrorActionPreference = 'Stop'
if ($Target -ne 'windows') {
    Write-Warning 'Unix outputs are core binaries, not standalone GUI apps. Build the native helper and run scripts/package-unix.sh before publishing the .pkg/.deb installers.'
}
$projectRoot = Split-Path -Parent $PSScriptRoot
$buildRevision = (& git -C $projectRoot rev-parse --verify HEAD).Trim().ToLowerInvariant()
if ($LASTEXITCODE -ne 0 -or $buildRevision -notmatch '^[0-9a-f]{40}$') {
    throw 'A valid Git commit is required for a traceable desktop build.'
}
$buildRecords = @()
$commitLinkerFlag = "-X github.com/yudesk/yudesk/internal/releaseinfo.BuildCommit=$buildRevision"
$components = @('yudesk')
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
    throw 'Go with toolchain auto-download support, or Docker, is required.'
}

$previousGoToolchain = $env:GOTOOLCHAIN
if ($localGo) { $env:GOTOOLCHAIN = "go$GoVersion" }
$goImage = "golang:$GoVersion"

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
            $linkerFlags = if ($buildTarget.OS -eq 'windows') { "-s -w -H=windowsgui $commitLinkerFlag" } else { "-s -w $commitLinkerFlag" }
            if ($localGo) {
                $previousGOOS, $previousGOARCH = $env:GOOS, $env:GOARCH
                $env:GOOS, $env:GOARCH = $buildTarget.OS, $buildTarget.Arch
                try {
                    & go build -buildvcs=false -trimpath -ldflags $linkerFlags -o $output "./cmd/$component"
                    if ($LASTEXITCODE -ne 0) { throw "build failed: $component" }
                } finally {
                    $env:GOOS, $env:GOARCH = $previousGOOS, $previousGOARCH
                }
            } else {
                $containerOutput = "/src/dist/$($buildTarget.OS)-$($buildTarget.Arch)/$component$suffix"
                & docker run --rm -e "GOOS=$($buildTarget.OS)" -e "GOARCH=$($buildTarget.Arch)" -v "${projectRoot}:/src" -w /src -v yudesk-gomod:/go/pkg/mod -v yudesk-gocache:/root/.cache/go-build $goImage sh -lc "/usr/local/go/bin/go build -buildvcs=false -trimpath -ldflags='$linkerFlags' -o '$containerOutput' './cmd/$component'"
                if ($LASTEXITCODE -ne 0) { throw "build failed: $component" }
            }
        }
        $coreName = if ($buildTarget.OS -eq 'windows') { 'yudesk.exe' } else { 'yudesk' }
        $corePath = Join-Path $outputDir $coreName
        $coreHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $corePath).Hash.ToLowerInvariant()
        if ($buildTarget.OS -eq 'windows') {
            # The public Windows download is a real one-window installer. Keep
            # the application payload separate while building, then append it
            # to the GUI setup stub with an authenticated footer.
            $clientOutput = Join-Path $outputDir 'yudesk-client.exe'
            $setupStub = Join-Path $outputDir 'yudesk-setup-stub.exe'
            $installerOutput = Join-Path $outputDir 'yudesk.exe'
            Move-Item -LiteralPath $installerOutput -Destination $clientOutput -Force
            if ($localGo) {
                $previousGOOS, $previousGOARCH = $env:GOOS, $env:GOARCH
                $env:GOOS, $env:GOARCH = 'windows', $buildTarget.Arch
                try {
                    & go build -trimpath -ldflags '-s -w -H=windowsgui' -o $setupStub './cmd/yudesk-setup'
                    if ($LASTEXITCODE -ne 0) { throw 'build failed: yudesk-setup' }
                } finally {
                    $env:GOOS, $env:GOARCH = $previousGOOS, $previousGOARCH
                }
            } else {
                $containerStub = "/src/dist/windows-$($buildTarget.Arch)/yudesk-setup-stub.exe"
                & docker run --rm -e GOOS=windows -e "GOARCH=$($buildTarget.Arch)" -v "${projectRoot}:/src" -w /src -v yudesk-gomod:/go/pkg/mod -v yudesk-gocache:/root/.cache/go-build $goImage sh -lc "/usr/local/go/bin/go build -trimpath -ldflags='-s -w -H=windowsgui' -o '$containerStub' './cmd/yudesk-setup'"
                if ($LASTEXITCODE -ne 0) { throw 'build failed: yudesk-setup' }
            }
            $packager = Start-Process -FilePath $setupStub -ArgumentList @('-package', ('"' + $clientOutput + '"'), '-output', ('"' + $installerOutput + '"')) -WindowStyle Hidden -Wait -PassThru
            if ($packager.ExitCode -ne 0 -or -not (Test-Path -LiteralPath $installerOutput -PathType Leaf)) {
                throw 'Windows installer packaging failed'
            }
            Remove-Item -LiteralPath $clientOutput, $setupStub -Force
        }
        $artifactPath = Join-Path $outputDir $coreName
        $buildRecords += [ordered]@{
            os = $buildTarget.OS
            arch = $buildTarget.Arch
            revision = $buildRevision
            coreSha256 = $coreHash
            artifactSha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $artifactPath).Hash.ToLowerInvariant()
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
                & go build -buildvcs=false -trimpath -ldflags "-s -w $commitLinkerFlag" -o $serverOutput './cmd/yudesk-relay'
                if ($LASTEXITCODE -ne 0) { throw 'server build failed' }
            } finally {
                $env:GOOS, $env:GOARCH = $previousGOOS, $previousGOARCH
            }
        } else {
            & docker run --rm -e GOOS=darwin -e GOARCH=arm64 -v "${projectRoot}:/src" -w /src -v yudesk-gomod:/go/pkg/mod -v yudesk-gocache:/root/.cache/go-build $goImage sh -lc "/usr/local/go/bin/go build -buildvcs=false -trimpath -ldflags='-s -w $commitLinkerFlag' -o '/src/dist/server/yudesk-relay' './cmd/yudesk-relay'"
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
    # Hash only the artifacts produced for this invocation. A recursive scan
    # can accidentally publish files from old deployment staging directories
    # whose final folder happens to be named windows-amd64 or darwin-arm64.
    $hashes = foreach ($buildTarget in $targets) {
        $suffix = if ($buildTarget.OS -eq 'windows') { '.exe' } else { '' }
        foreach ($component in $components) {
            $relative = "$($buildTarget.OS)-$($buildTarget.Arch)/$component$suffix"
            $publishedArtifact = Join-Path $distRoot $relative
            if (-not (Test-Path -LiteralPath $publishedArtifact -PathType Leaf)) {
                throw "missing release artifact: $relative"
            }
            $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $publishedArtifact).Hash.ToLowerInvariant()
            "$hash  $($relative.Replace('\', '/'))"
        }
    }
    $hashes = $hashes | Sort-Object
    $checksumPath = Join-Path $projectRoot 'dist/SHA256SUMS.txt'
    [System.IO.File]::WriteAllText($checksumPath, (($hashes -join "`n") + "`n"), [System.Text.Encoding]::ASCII)
    $buildManifest = [ordered]@{ version = '2.0.0'; revision = $buildRevision; targets = $buildRecords }
    [System.IO.File]::WriteAllText((Join-Path $projectRoot 'dist/desktop-core-builds.json'), (($buildManifest | ConvertTo-Json -Depth 5 -Compress) + "`n"), [System.Text.UTF8Encoding]::new($false))
    Write-Host "Build complete: $(Join-Path $projectRoot 'dist')"
} finally {
    $env:GOTOOLCHAIN = $previousGoToolchain
    Pop-Location
}
