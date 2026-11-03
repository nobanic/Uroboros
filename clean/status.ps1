[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$cleanDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$composeFile = Join-Path $cleanDir 'compose.yml'
$isWindowsHost = ($PSVersionTable.PSEdition -eq 'Desktop') -or
    [Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([Runtime.InteropServices.OSPlatform]::Windows)
if (-not [string]::IsNullOrWhiteSpace($env:ASTRA_RUNTIME_DIR)) {
    $runtimeRoot = [IO.Path]::GetFullPath($env:ASTRA_RUNTIME_DIR)
}
elseif ($isWindowsHost) {
    $runtimeRoot = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Astra'
}
else {
    if ([string]::IsNullOrWhiteSpace($env:HOME)) {
        throw 'HOME must be set, or set ASTRA_RUNTIME_DIR explicitly.'
    }
    $stateHome = if ([string]::IsNullOrWhiteSpace($env:XDG_STATE_HOME)) {
        Join-Path $env:HOME '.local/state'
    }
    else {
        $env:XDG_STATE_HOME
    }
    $runtimeRoot = Join-Path $stateHome 'astra'
}
$runtimeEnv = Join-Path $runtimeRoot 'runtime.env'

if (-not (Test-Path -LiteralPath $runtimeEnv)) {
    Write-Host 'Uroboros 0.5 has not been initialized yet.'
    exit 0
}

& docker compose --env-file $runtimeEnv -f $composeFile ps
if ($LASTEXITCODE -ne 0) {
    throw "docker compose ps failed with exit code $LASTEXITCODE"
}
$line = [IO.File]::ReadAllLines($runtimeEnv) |
    Where-Object { $_.StartsWith('UROBOROS_PORT=') } |
    Select-Object -First 1
[int]$port = 0
if (-not $line -or -not [int]::TryParse($line.Substring('UROBOROS_PORT='.Length).Trim(), [ref]$port) -or
    $port -lt 1 -or $port -gt 65535) {
    throw "Invalid UROBOROS_PORT in $runtimeEnv"
}
try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/health" -TimeoutSec 5
    $health | ConvertTo-Json -Depth 4
}
catch {
    Write-Warning 'The local HTTP endpoint is not responding.'
}
