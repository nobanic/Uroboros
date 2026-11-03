param(
    [string]$WheelDestination = (Join-Path $PSScriptRoot "capa-wheelhouse"),
    [string]$RulesDestination = (Join-Path $PSScriptRoot "capa-rules-corpus"),
    [string]$SignaturesDestination = (Join-Path $PSScriptRoot "capa-signatures-corpus")
)

$ErrorActionPreference = "Stop"
$capaCommit = "7a79f799a70f052e8382cdbeaec8115daba14a5b"
$rulesCommit = "2af9fbfc1c9b4634dbeb76b5d34fca9389fa7f80"
$rulesManifestSHA256 = "1910582fdcc7fd0bff198cfe07f4f1e3ceaf3acbd0c840d52d6507009d2bb5e2"
$signaturesManifestSHA256 = "1b28cbecf167dbf458268ba0c282202d1347f663bba3a6089e0846391f3e083e"
$lockPath = Join-Path $PSScriptRoot "capa-requirements.lock"

function Get-CorpusManifestHash {
    param([string]$Root, [string[]]$Extensions)
    $rootPath = (Resolve-Path -LiteralPath $Root).Path.TrimEnd('\')
    [string[]]$relativePaths = @(Get-ChildItem -LiteralPath $rootPath -Recurse -File | Where-Object {
        $Extensions -contains $_.Extension.ToLowerInvariant()
    } | ForEach-Object { $_.FullName.Substring($rootPath.Length + 1).Replace('\', '/') })
    [Array]::Sort($relativePaths, [StringComparer]::Ordinal)
    $lines = foreach ($relative in $relativePaths) {
        $file = Get-Item -LiteralPath (Join-Path $rootPath $relative)
        $hash = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        "$relative`t$($file.Length)`t$hash"
    }
    $manifest = (($lines -join "`n") + "`n")
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $digest = $sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($manifest))
    } finally {
        $sha.Dispose()
    }
    [pscustomobject]@{
        Hash = ([BitConverter]::ToString($digest)).Replace('-', '').ToLowerInvariant()
        Count = $relativePaths.Count
        Bytes = ($relativePaths | ForEach-Object { (Get-Item -LiteralPath (Join-Path $rootPath $_)).Length } | Measure-Object -Sum).Sum
    }
}

function Assert-EmptyOrMissingDirectory {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        if (@(Get-ChildItem -LiteralPath $Path -Force).Count -ne 0) {
            throw "Refusing to overwrite non-empty resource directory: $Path"
        }
    } else {
        New-Item -ItemType Directory -Path $Path | Out-Null
    }
}

New-Item -ItemType Directory -Force -Path $WheelDestination | Out-Null
$expectedWheelFiles = @()
foreach ($line in Get-Content -LiteralPath $lockPath) {
    $trimmed = $line.Trim()
    if ($trimmed -eq '' -or $trimmed.StartsWith('#')) {
        continue
    }
    if ($trimmed -notmatch '^([^= ]+)==([^ ]+) --hash=sha256:([0-9a-f]{64})$') {
        throw "Invalid capa wheel lock line: $trimmed"
    }
    $package = $Matches[1]
    $version = $Matches[2]
    $expectedHash = $Matches[3]
    $metadata = Invoke-RestMethod -UseBasicParsing -Uri "https://pypi.org/pypi/$package/$version/json"
    $matches = @($metadata.urls | Where-Object {
        $_.digests.sha256.ToLowerInvariant() -eq $expectedHash -and $_.packagetype -eq 'bdist_wheel'
    })
    if ($matches.Count -ne 1) {
        throw "Expected exactly one locked wheel for $package==$version"
    }
    $fileName = [string]$matches[0].filename
    $target = Join-Path $WheelDestination $fileName
    $expectedWheelFiles += $fileName
    if (Test-Path -LiteralPath $target) {
        $actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expectedHash) {
            throw "Existing wheel has the wrong SHA-256: $target"
        }
        continue
    }
    $partial = "$target.partial"
    Invoke-WebRequest -UseBasicParsing -Uri $matches[0].url -OutFile $partial
    $actual = (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expectedHash) {
        Remove-Item -LiteralPath $partial -Force
        throw "Downloaded wheel SHA-256 mismatch: $fileName"
    }
    Move-Item -LiteralPath $partial -Destination $target
}
$unexpected = @(Get-ChildItem -LiteralPath $WheelDestination -Filter '*.whl' -File | Where-Object {
    $expectedWheelFiles -notcontains $_.Name
})
if ($unexpected.Count -ne 0) {
    throw "Unexpected wheel(s) in the capa wheelhouse: $($unexpected.Name -join ', ')"
}

Assert-EmptyOrMissingDirectory -Path $RulesDestination
Assert-EmptyOrMissingDirectory -Path $SignaturesDestination
$temporary = Join-Path $PSScriptRoot (".fetch-capa-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $rulesArchive = Join-Path $temporary 'capa-rules.zip'
    $capaArchive = Join-Path $temporary 'capa.zip'
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/mandiant/capa-rules/archive/$rulesCommit.zip" -OutFile $rulesArchive
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/mandiant/capa/archive/$capaCommit.zip" -OutFile $capaArchive
    $rulesExtract = Join-Path $temporary 'rules'
    $capaExtract = Join-Path $temporary 'capa'
    Expand-Archive -LiteralPath $rulesArchive -DestinationPath $rulesExtract
    Expand-Archive -LiteralPath $capaArchive -DestinationPath $capaExtract
    $rulesRoot = @(Get-ChildItem -LiteralPath $rulesExtract -Directory)
    $capaRoot = @(Get-ChildItem -LiteralPath $capaExtract -Directory)
    if ($rulesRoot.Count -ne 1 -or $capaRoot.Count -ne 1) {
        throw "Unexpected GitHub source archive layout"
    }
    foreach ($file in Get-ChildItem -LiteralPath $rulesRoot[0].FullName -Recurse -File -Filter '*.yml') {
        $relative = $file.FullName.Substring($rulesRoot[0].FullName.Length + 1)
        $target = Join-Path $RulesDestination $relative
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $target) | Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination $target
    }
    foreach ($file in Get-ChildItem -LiteralPath (Join-Path $capaRoot[0].FullName 'sigs') -File | Where-Object {
        $_.Extension.ToLowerInvariant() -in @('.sig', '.pat')
    }) {
        Copy-Item -LiteralPath $file.FullName -Destination $SignaturesDestination
    }
    $rulesManifest = Get-CorpusManifestHash -Root $RulesDestination -Extensions @('.yml')
    $signatureManifest = Get-CorpusManifestHash -Root $SignaturesDestination -Extensions @('.sig', '.pat')
    if ($rulesManifest.Hash -ne $rulesManifestSHA256 -or $rulesManifest.Count -ne 1045 -or $rulesManifest.Bytes -ne 836221) {
        throw "capa-rules corpus identity mismatch: $($rulesManifest | ConvertTo-Json -Compress)"
    }
    if ($signatureManifest.Hash -ne $signaturesManifestSHA256 -or $signatureManifest.Count -ne 3 -or $signatureManifest.Bytes -ne 15179585) {
        throw "capa signature corpus identity mismatch: $($signatureManifest | ConvertTo-Json -Compress)"
    }
} finally {
    if (Test-Path -LiteralPath $temporary) {
        Remove-Item -LiteralPath $temporary -Recurse -Force
    }
}

Write-Output "capa wheels=$($expectedWheelFiles.Count) rules=1045 signatures=3"
