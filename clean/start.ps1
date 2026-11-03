[CmdletBinding()]
param(
    [switch]$NoBuild,
    [switch]$NoWait,
    [switch]$PreflightOnly
)

$ErrorActionPreference = 'Stop'
$cleanDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$appDir = Split-Path -Parent $cleanDir
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
$imageDir = Join-Path $cleanDir 'runtime-images'
$languageCheck = Join-Path $cleanDir 'verify-language.ps1'

function New-RandomHex([int]$ByteCount) {
    $bytes = New-Object byte[] $ByteCount
    $generator = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
    }
    finally {
        $generator.Dispose()
    }
    return ([BitConverter]::ToString($bytes)).Replace('-', '').ToLowerInvariant()
}

function Get-SourceRevision {
    $revision = 'local-clean-0.5'
    try {
        $candidate = (& git -C $appDir rev-parse HEAD 2>$null).Trim()
        if ($candidate -match '^[0-9a-f]{40}$') {
            return $candidate
        }
    }
    catch {
        # The exported directory can run without Git metadata.
    }
    $releaseSource = Join-Path $appDir 'RELEASE_SOURCE'
    if (Test-Path -LiteralPath $releaseSource -PathType Leaf) {
        $candidate = [IO.File]::ReadAllText($releaseSource).Trim()
        if ($candidate -match '^[0-9a-f]{40}$') {
            return $candidate
        }
    }
    return $revision
}

function Set-RuntimeSourceRevision([string]$Path, [string]$Revision) {
    $lines = [Collections.Generic.List[string]]::new()
    $lines.AddRange([string[]][IO.File]::ReadAllLines($Path))
    $updated = $false
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index].StartsWith('UROBOROS_SOURCE_REVISION=')) {
            $lines[$index] = "UROBOROS_SOURCE_REVISION=$Revision"
            $updated = $true
            break
        }
    }
    if (-not $updated) {
        $lines.Add("UROBOROS_SOURCE_REVISION=$Revision")
    }
    [IO.File]::WriteAllLines($Path, $lines, [Text.UTF8Encoding]::new($false))
}

function Invoke-DockerCompose([string[]]$ComposeArgs) {
    & docker compose --env-file $runtimeEnv -f $composeFile @ComposeArgs
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed with exit code $LASTEXITCODE"
    }
}

function Get-RuntimePort {
    $line = [IO.File]::ReadAllLines($runtimeEnv) |
        Where-Object { $_.StartsWith('UROBOROS_PORT=') } |
        Select-Object -First 1
    [int]$port = 0
    if (-not $line -or -not [int]::TryParse($line.Substring('UROBOROS_PORT='.Length).Trim(), [ref]$port) -or
        $port -lt 1 -or $port -gt 65535) {
        throw "Invalid UROBOROS_PORT in $runtimeEnv"
    }
    return $port
}

function Test-DockerImage([string]$Reference) {
    # A cache miss is expected on a clean Docker host. Windows PowerShell turns
    # native stderr into an ErrorRecord, so the global Stop policy would
    # otherwise abort before Ensure-Image can use an approved distribution
    # source. Keep every other startup error fail-closed.
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'SilentlyContinue'
        & docker image inspect $Reference *> $null
        return ($LASTEXITCODE -eq 0)
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Ensure-Image([string]$Reference, [string]$ArchiveName, [string]$ArchiveSHA256) {
    if (Test-DockerImage -Reference $Reference) {
        return
    }
    $archive = Join-Path $imageDir $ArchiveName
    if (-not (Test-Path -LiteralPath $archive)) {
        throw "Required accepted binary runtime image is missing: $Reference. Expected offline archive: $archive"
    }
    $actualSHA256 = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualSHA256 -ne $ArchiveSHA256) {
        throw "Offline image archive checksum mismatch: $archive"
    }
    Write-Host "Loading accepted binary runtime image from $archive ..."
    & docker image load --input $archive
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to load $archive"
    }
    if (-not (Test-DockerImage -Reference $Reference)) {
        throw "The archive did not provide the exact required image: $Reference"
    }
}

& $languageCheck -Root $appDir

& docker version *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Engine is not available.'
}
& docker compose version *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker Compose v2 is not available.'
}
$engine = (& docker info --format '{{.OSType}}|{{.Architecture}}' 2>$null).Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($engine)) {
    throw 'Docker engine information is unavailable.'
}
if ($engine -notmatch '^linux\|(amd64|x86_64)$') {
    if ($engine.StartsWith('linux|')) {
        throw "The clean B0.5 runtime currently requires an amd64/x86_64 Docker engine; detected: $engine"
    }
    throw "The clean bundle requires a Linux-container Docker engine; detected: $engine"
}

New-Item -ItemType Directory -Path $runtimeRoot -Force | Out-Null
if (-not $isWindowsHost) {
    & chmod 700 $runtimeRoot
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to protect runtime directory: $runtimeRoot"
    }
}
$sourceRevision = Get-SourceRevision
if (-not (Test-Path -LiteralPath $runtimeEnv)) {
    [IO.File]::WriteAllLines($runtimeEnv, @(
        '# Generated locally. Contains only internal Docker service credentials; no LLM/provider API key.'
        "POSTGRES_PASSWORD=$(New-RandomHex 32)"
        "ARSENAL_TOKEN=$(New-RandomHex 32)"
        "API_LAB_TOKEN=$(New-RandomHex 32)"
        'UROBOROS_PORT=8805'
        "UROBOROS_SOURCE_REVISION=$sourceRevision"
    ), [Text.UTF8Encoding]::new($false))
    Write-Host "Created private runtime state: $runtimeEnv"
}
else {
    Set-RuntimeSourceRevision -Path $runtimeEnv -Revision $sourceRevision
}
if (-not $isWindowsHost) {
    & chmod 600 $runtimeEnv
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to protect runtime environment: $runtimeEnv"
    }
}

Ensure-Image `
    'uroboros-b0/binary-broker@sha256:0fc4e1bfe1c398f2371aa7b0f18c8b6a2fdbf3e3544b6ea3c3576fc734f58173' `
    'binary-broker-b05.tar' `
    'f12520c790e04e3cddb0766ab8cc93213563c78c61a28f5874b61eeb6513ef6f'
Ensure-Image `
    'uroboros-b0/binary-worker@sha256:142bdcf03aa22e6a27fead6dedf89391155a46210375f007871bd09726315514' `
    'binary-worker-b05.tar' `
    '5cc2a489c15b2a85f8a65acaede4fc087040539840f330aa6617191dbb060123'

if ($PreflightOnly) {
    Write-Host 'Cold-start preflight passed; exact B0.5 runtime images are available.'
    return
}

Invoke-DockerCompose @('config', '--quiet')
$upArgs = @('up', '-d')
if (-not $NoBuild) {
    $upArgs += '--build'
}
Invoke-DockerCompose $upArgs

if (-not $NoWait) {
    Write-Host 'Waiting for tool bootstrap ...'
    $ready = $false
    for ($attempt = 0; $attempt -lt 180; $attempt++) {
        $containerId = (& docker compose --env-file $runtimeEnv -f $composeFile ps -a -q bootstrap).Trim()
        if ($containerId) {
            $state = (& docker inspect --format '{{.State.Status}}|{{.State.ExitCode}}' $containerId).Trim()
            if ($state -eq 'exited|0') {
                $ready = $true
                break
            }
            if ($state -match '^exited\|') {
                & docker compose --env-file $runtimeEnv -f $composeFile logs --no-color bootstrap
                throw "Tool bootstrap failed ($state)."
            }
        }
        Start-Sleep -Seconds 2
    }
    if (-not $ready) {
        & docker compose --env-file $runtimeEnv -f $composeFile logs --no-color bootstrap
        throw 'Tool bootstrap did not finish within six minutes.'
    }
}

$health = $null
$auth = $null
$ready = $false
$port = Get-RuntimePort
$baseUrl = "http://127.0.0.1:$port"
for ($attempt = 0; $attempt -lt 120; $attempt++) {
    try {
        $health = Invoke-RestMethod -Uri "$baseUrl/api/health" -TimeoutSec 5
        $auth = Invoke-RestMethod -Uri "$baseUrl/api/auth/status" -TimeoutSec 5
        if ($health.ok -and -not $health.auth_disabled -and -not $auth.disabled) {
            $ready = $true
            break
        }
    }
    catch {
        # The final auth-enabled container may still be replacing the one-shot bootstrap.
    }
    Start-Sleep -Seconds 2
}
if (-not $ready) {
    & docker compose --env-file $runtimeEnv -f $composeFile logs --no-color uroboros
    throw 'The control plane did not confirm auth-enabled mode within four minutes.'
}

$entryUrl = if ($auth.initialized) {
    "$baseUrl/login/"
}
else {
    "$baseUrl/setup/"
}
Write-Host ''
Write-Host "Uroboros 0.5 is ready: $entryUrl" -ForegroundColor Green
if ($auth.initialized) {
    Write-Host 'Authentication is enabled. Sign in with the local operator account.'
}
else {
    Write-Host 'Authentication is enabled. Create the first local operator password on the setup page.'
}
Write-Host 'No LLM/provider API key is configured.'
