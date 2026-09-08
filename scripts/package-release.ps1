param([ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version='2.0.0')
$ErrorActionPreference='Stop'
$project=Split-Path -Parent $PSScriptRoot
$dist=Join-Path $project 'dist'
$source=Get-Content -Raw -LiteralPath (Join-Path $project 'internal/releaseinfo/release.go')
if($source -notmatch ('const Version = "'+[Regex]::Escape($Version)+'"')){throw 'Version differs from compiled product source'}
$files=@('windows-amd64/yudesk.exe','linux-amd64/yudesk','darwin-amd64/yudesk','darwin-arm64/yudesk','server/yudesk-relay','SHA256SUMS.txt')
foreach($relative in $files){if(-not (Test-Path -LiteralPath (Join-Path $dist $relative) -PathType Leaf)){throw "Missing release file: $relative"}}
# Explicit publication metadata is produced only by packaging, not every build.
$metadata=@{version=$Version;publishedAt=[DateTimeOffset]::UtcNow.ToString('o')}|ConvertTo-Json -Compress
[IO.File]::WriteAllText((Join-Path $dist 'release.json'),$metadata+"`n",[Text.UTF8Encoding]::new($false))
$archive=Join-Path $dist "YuDesk-$Version-desktop.tgz"
& tar -czf $archive -C $dist @files release.json
if($LASTEXITCODE -ne 0){throw 'Packaging failed'}
$flat=foreach($relative in $files[0..3]){
 $name='YuDesk-'+$Version+'-'+$relative.Split('/')[0]+[IO.Path]::GetExtension($relative)
 (Get-FileHash -LiteralPath (Join-Path $dist $relative) -Algorithm SHA256).Hash.ToLowerInvariant()+'  '+$name
}
[IO.File]::WriteAllText((Join-Path $dist 'GITHUB-SHA256SUMS.txt'),($flat -join "`n")+"`n",[Text.UTF8Encoding]::new($false))
Write-Output "Packaged $archive"
Get-FileHash -LiteralPath $archive -Algorithm SHA256
