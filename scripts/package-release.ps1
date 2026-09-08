param([ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version='2.0.0')
$ErrorActionPreference='Stop'
$project=Split-Path -Parent $PSScriptRoot
$dist=Join-Path $project 'dist'
$source=Get-Content -Raw -LiteralPath (Join-Path $project 'internal/releaseinfo/release.go')
if($source -notmatch ('const Version = "'+[Regex]::Escape($Version)+'"')){throw 'Version differs from compiled product source'}
$files=@('windows-amd64/yudesk.exe','linux-amd64/yudesk.deb','darwin-amd64/yudesk.pkg','darwin-arm64/yudesk.pkg','android/yudesk.apk','server/yudesk-relay','THIRD_PARTY_NOTICES.txt')
foreach($relative in $files){if(-not (Test-Path -LiteralPath (Join-Path $dist $relative) -PathType Leaf)){throw "Missing release file: $relative"}}
# Native Unix helpers and desktop integration are included by package-unix.sh;
# never publish the naked Go executables as working GUI installers.
$checksums=foreach($relative in $files){(Get-FileHash -LiteralPath (Join-Path $dist $relative) -Algorithm SHA256).Hash.ToLowerInvariant()+'  '+$relative}
[IO.File]::WriteAllText((Join-Path $dist 'SHA256SUMS.txt'),($checksums -join "`n")+"`n",[Text.UTF8Encoding]::new($false))
# Explicit publication metadata is produced only by packaging, not every build.
$metadata=@{version=$Version;publishedAt=[DateTimeOffset]::UtcNow.ToString('o')}|ConvertTo-Json -Compress
[IO.File]::WriteAllText((Join-Path $dist 'release.json'),$metadata+"`n",[Text.UTF8Encoding]::new($false))
$archive=Join-Path $dist "YuDesk-$Version-all-platforms.tgz"
& tar -czf $archive -C $dist @files release.json SHA256SUMS.txt
if($LASTEXITCODE -ne 0){throw 'Packaging failed'}
$flat=foreach($relative in $files[0..4]){
 $name='YuDesk-'+$Version+'-'+$relative.Split('/')[0]+[IO.Path]::GetExtension($relative)
 if($relative.StartsWith('android/')){$name='YuDesk-'+$Version+'-android-preview.apk'}
 (Get-FileHash -LiteralPath (Join-Path $dist $relative) -Algorithm SHA256).Hash.ToLowerInvariant()+'  '+$name
}
[IO.File]::WriteAllText((Join-Path $dist 'GITHUB-SHA256SUMS.txt'),($flat -join "`n")+"`n",[Text.UTF8Encoding]::new($false))
Write-Output "Packaged $archive"
Get-FileHash -LiteralPath $archive -Algorithm SHA256
