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
    throw "Runtime environment does not exist: $runtimeEnv"
}

. (Join-Path $cleanDir 'update-runtime.ps1')
$updatePaths = Get-UpdateRuntimePaths -SourceRoot (Split-Path -Parent $cleanDir) -PrivateRoot $runtimeRoot
$composeFile = Join-Path $updatePaths.Source 'clean/compose.yml'
& docker compose --env-file $runtimeEnv -f $composeFile down
if ($LASTEXITCODE -ne 0) {
    throw "docker compose down failed with exit code $LASTEXITCODE"
}
Write-Host 'Uroboros 0.5 stopped. Its named volumes were preserved.'
