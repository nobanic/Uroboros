param(
    [string]$WheelDestination = (Join-Path $PSScriptRoot "floss-wheelhouse")
)

$ErrorActionPreference = "Stop"
$lockPath = Join-Path $PSScriptRoot "floss-requirements.lock"
$localWheelRoot = Join-Path $PSScriptRoot "floss-local-wheels"
$localWheels = @{
    "binary2strings" = "binary2strings-0.1.13-cp312-cp312-linux_x86_64.whl"
    "halo" = "halo-0.0.31-py3-none-any.whl"
}

New-Item -ItemType Directory -Force -Path $WheelDestination | Out-Null
$expectedWheelFiles = @()
foreach ($line in Get-Content -LiteralPath $lockPath) {
    $trimmed = $line.Trim()
    if ($trimmed -eq '' -or $trimmed.StartsWith('#')) {
        continue
    }
    if ($trimmed -notmatch '^([^= ]+)==([^ ]+) --hash=sha256:([0-9a-f]{64})$') {
        throw "Invalid FLOSS wheel lock line: $trimmed"
    }
    $package = $Matches[1]
    $version = $Matches[2]
    $expectedHash = $Matches[3]
    if ($localWheels.ContainsKey($package)) {
        $fileName = $localWheels[$package]
        $source = Join-Path $localWheelRoot $fileName
        if (-not (Test-Path -LiteralPath $source)) {
            throw "Missing reviewed local FLOSS wheel: $source"
        }
        $actual = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actual -ne $expectedHash) {
            throw "Reviewed local FLOSS wheel SHA-256 mismatch: $fileName"
        }
        $target = Join-Path $WheelDestination $fileName
        if (-not (Test-Path -LiteralPath $target)) {
            Copy-Item -LiteralPath $source -Destination $target
        }
    } else {
        $metadata = Invoke-RestMethod -UseBasicParsing -Uri "https://pypi.org/pypi/$package/$version/json"
        $matches = @($metadata.urls | Where-Object {
            $_.digests.sha256.ToLowerInvariant() -eq $expectedHash -and $_.packagetype -eq 'bdist_wheel'
        })
        if ($matches.Count -ne 1) {
            throw "Expected exactly one locked wheel for $package==$version"
        }
        $fileName = [string]$matches[0].filename
        $target = Join-Path $WheelDestination $fileName
        if (-not (Test-Path -LiteralPath $target)) {
            $partial = "$target.partial"
            Invoke-WebRequest -UseBasicParsing -Uri $matches[0].url -OutFile $partial
            $actual = (Get-FileHash -LiteralPath $partial -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($actual -ne $expectedHash) {
                Remove-Item -LiteralPath $partial -Force
                throw "Downloaded FLOSS wheel SHA-256 mismatch: $fileName"
            }
            Move-Item -LiteralPath $partial -Destination $target
        }
    }
    $actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expectedHash) {
        throw "Existing FLOSS wheel has the wrong SHA-256: $target"
    }
    $expectedWheelFiles += $fileName
}
$unexpected = @(Get-ChildItem -LiteralPath $WheelDestination -Filter '*.whl' -File | Where-Object {
    $expectedWheelFiles -notcontains $_.Name
})
if ($unexpected.Count -ne 0) {
    throw "Unexpected wheel(s) in the FLOSS wheelhouse: $($unexpected.Name -join ', ')"
}
Write-Output "FLOSS wheels=$($expectedWheelFiles.Count)"
