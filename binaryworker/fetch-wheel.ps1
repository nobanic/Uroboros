param(
    [string]$Destination = (Join-Path $PSScriptRoot "wheelhouse")
)

$ErrorActionPreference = "Stop"
$fileName = "lief-1.0.0-cp312-abi3-manylinux_2_28_x86_64.whl"
$expected = "41ff868f5328fc8e5237a9e1f4718590e0ee9180c1422b3e4948b7116ba4517c"
$uri = "https://files.pythonhosted.org/packages/cd/d7/6e8135da80a63d8ee413f169eda8c331817bf6f1496fc5647722b105aa19/$fileName"

New-Item -ItemType Directory -Force -Path $Destination | Out-Null
$target = Join-Path $Destination $fileName
Invoke-WebRequest -UseBasicParsing -Uri $uri -OutFile $target
$actual = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) {
    Remove-Item -LiteralPath $target -Force
    throw "LIEF wheel SHA-256 mismatch"
}
Write-Output $target
