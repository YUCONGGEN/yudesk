param([ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version='2.0.0')
$ErrorActionPreference='Stop'
$project=Split-Path -Parent $PSScriptRoot
$dist=Join-Path $project 'dist'
$source=Get-Content -Raw -LiteralPath (Join-Path $project 'internal/releaseinfo/release.go')
if($source -notmatch ('const Version = "'+[Regex]::Escape($Version)+'"')){throw 'Version differs from compiled product source'}
$buildManifest=Join-Path $dist 'desktop-core-builds.json'
if(-not (Test-Path -LiteralPath $buildManifest -PathType Leaf)){throw 'Missing desktop build provenance manifest'}
$build=Get-Content -Raw -LiteralPath $buildManifest | ConvertFrom-Json
$head=(& git -C $project rev-parse --verify HEAD).Trim().ToLowerInvariant()
if($build.version -ne $Version -or $build.revision -ne $head){throw 'Desktop builds do not come from the current Git commit'}
$expectedTargets=@('windows/amd64','linux/amd64','darwin/amd64','darwin/arm64')
$actualTargets=@($build.targets | ForEach-Object { $_.os+'/'+$_.arch } | Sort-Object -Unique)
if(Compare-Object ($expectedTargets | Sort-Object) $actualTargets){throw 'Desktop build provenance is incomplete'}
if(@($build.targets | Where-Object { $_.revision -ne $head }).Count -ne 0){throw 'Mixed desktop revisions are not publishable'}
$unixPackages=@(
 @{target='linux/amd64'; package='linux-amd64/yudesk.deb'; provenance='linux-amd64/yudesk.provenance.json'},
 @{target='darwin/amd64'; package='darwin-amd64/yudesk.pkg'; provenance='darwin-amd64/yudesk.provenance.json'},
 @{target='darwin/arm64'; package='darwin-arm64/yudesk.pkg'; provenance='darwin-arm64/yudesk.provenance.json'}
)
foreach($entry in $unixPackages){
 $provenancePath=Join-Path $dist $entry.provenance
 if(-not (Test-Path -LiteralPath $provenancePath -PathType Leaf)){throw "Missing package provenance: $($entry.provenance)"}
 $provenance=Get-Content -Raw -LiteralPath $provenancePath | ConvertFrom-Json
 $core=@($build.targets | Where-Object { ($_.os+'/'+$_.arch) -eq $entry.target })[0]
 $packageHash=(Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $dist $entry.package)).Hash.ToLowerInvariant()
 if($provenance.version -ne $Version -or $provenance.buildCommit -ne $head -or $provenance.coreSha256 -ne $core.coreSha256 -or $provenance.packageSha256 -ne $packageHash){
  throw "Package provenance mismatch: $($entry.package)"
 }
}
$files=@('windows-amd64/yudesk.exe','linux-amd64/yudesk.deb','darwin-amd64/yudesk.pkg','darwin-arm64/yudesk.pkg','android/yudesk.apk','server/yudesk-relay','THIRD_PARTY_NOTICES.txt','desktop-core-builds.json','linux-amd64/yudesk.provenance.json','darwin-amd64/yudesk.provenance.json','darwin-arm64/yudesk.provenance.json')
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
