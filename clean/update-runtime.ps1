# Resolve the last healthy managed source without storing private state in Git.
function Get-UpdateRuntimePaths([string]$SourceRoot, [string]$PrivateRoot) {
    $revision = ''
    try {
        $candidate = (& git -C $SourceRoot rev-parse HEAD 2>$null).Trim()
        if ($candidate -match '^[0-9a-f]{40}$') { $revision = $candidate }
    } catch { }
    if (-not $revision) {
        $marker = Join-Path $SourceRoot 'RELEASE_SOURCE'
        if (Test-Path -LiteralPath $marker -PathType Leaf) {
            $candidate = [IO.File]::ReadAllText($marker).Trim()
            if ($candidate -match '^[0-9a-f]{40}$') { $revision = $candidate }
        }
    }
    if (-not $revision) { throw 'The installation has no full source revision.' }
    $seed = $revision
    $stateDir = [IO.Path]::GetFullPath((Join-Path $PrivateRoot 'updates'))
    $activePath = Join-Path $stateDir 'active.json'
    if (Test-Path -LiteralPath $activePath -PathType Leaf) {
        $active = [IO.File]::ReadAllText($activePath) | ConvertFrom-Json
        if ($active.seed_revision -eq $seed) {
            if ($active.revision -notmatch '^[0-9a-f]{40}$' -or
                $active.source -notmatch ('^releases/' + [regex]::Escape($active.revision) + '/[a-z0-9][a-z0-9_.-]+$')) {
                throw 'The active update source pointer is invalid.'
            }
            $selected = [IO.Path]::GetFullPath((Join-Path $stateDir $active.source))
            if (-not $selected.StartsWith($stateDir + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase) -or
                -not (Test-Path -LiteralPath (Join-Path $selected 'clean/compose.yml') -PathType Leaf)) {
                throw 'The last healthy update source is missing; restore its private source snapshot.'
            }
            $SourceRoot = $selected
            $revision = $active.revision
        }
    }
    $env:UROBOROS_UPDATE_STATE_DIR = $stateDir.Replace('\', '/')
    $env:UROBOROS_UPDATE_INSTALL_DIR = ([IO.Path]::GetFullPath($SourceRoot)).Replace('\', '/')
    $env:UROBOROS_UPDATE_SEED = $seed
    $env:UROBOROS_SOURCE_REVISION = $revision
    return [pscustomobject]@{ Source = $SourceRoot; Revision = $revision; Seed = $seed; State = $stateDir }
}
