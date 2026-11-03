[CmdletBinding()]
param(
    [string]$Root
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Root)) {
    $Root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
}
$resolvedRoot = (Resolve-Path -LiteralPath $Root).Path
$legacyTokens = @(
    ('aut' + 'umn'),
    ('art' + 'ex'),
    ('art' + 'umn'),
    ('scope' + 'sentry')
)
$escapedLegacyTokens = $legacyTokens | ForEach-Object { [regex]::Escape($_) }
$legacyTokenPattern = [regex]::new(
    '(?<![A-Za-z0-9])(?:' + ($escapedLegacyTokens -join '|') + ')(?![A-Za-z0-9])',
    [Text.RegularExpressions.RegexOptions]::IgnoreCase
)
$agentContextTokens = @(
    ('cod' + 'ex'),
    ('claude' + '-code'),
    ('claude' + ' code')
)
$escapedAgentContextTokens = $agentContextTokens | ForEach-Object { [regex]::Escape($_) }
$agentContextPattern = [regex]::new(
    '(?<![A-Za-z0-9])(?:' + ($escapedAgentContextTokens -join '|') + ')(?![A-Za-z0-9])',
    [Text.RegularExpressions.RegexOptions]::IgnoreCase
)
$forbiddenScript = [regex]::new('[\u3400-\u4DBF\u4E00-\u9FFF\uF900-\uFAFF]')
$forbiddenMarks = [regex]::new('[\u3000-\u303F\uFE30-\uFE4F\uFF01-\uFF60]')
$ignoredPath = [regex]::new('(^|[\\/])(\.git|node_modules|\.next|out|dist|runtime-images|UroborOS)([\\/]|$)', [Text.RegularExpressions.RegexOptions]::IgnoreCase)
$removedPaths = @(
    'CHANGELOG.md',
    ('CONSOLE_GUIDE_' + 'CZ.md'),
    'handoff',
    ('.clau' + 'de'),
    ('.cod' + 'ex'),
    ('.super' + 'powers'),
    (('CLAU' + 'DE') + '.md'),
    (('COD' + 'EX') + '.md'),
    'AGENTS.md',
    'sidequestion/CONTEXT_BUDGET.md',
    'sidequestion/VALIDATION.md',
    'sidequestion/validation-2026-09-10.json'
)
$textExtensions = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
foreach ($extension in @(
    '.cjs', '.css', '.dockerignore', '.env', '.gitignore', '.go', '.html', '.js', '.json', '.jsx',
    '.md', '.mjs', '.mod', '.ps1', '.py', '.sh', '.sql', '.sum', '.toml', '.ts', '.tsx', '.txt',
    '.yaml', '.yml'
)) {
    [void]$textExtensions.Add($extension)
}
$textNames = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
foreach ($name in @('Dockerfile', 'LICENSE', 'Makefile', 'NOTICE', 'torrc')) {
    [void]$textNames.Add($name)
}

$violations = [Collections.Generic.List[string]]::new()
foreach ($relative in $removedPaths) {
    if (Test-Path -LiteralPath (Join-Path $resolvedRoot $relative)) {
        $violations.Add("removed path is present: $relative")
    }
}
$operatorGuidePath = Join-Path $resolvedRoot 'guide/README.md'
$applicationReadmePath = Join-Path $resolvedRoot 'README.md'
$screenshotRoot = Join-Path $resolvedRoot 'screenshots/0.5'
if (-not (Test-Path -LiteralPath $operatorGuidePath -PathType Leaf)) {
    $violations.Add('required operator guide is missing: guide/README.md')
}
else {
    $guideContent = [IO.File]::ReadAllText($operatorGuidePath)
    if (-not (Test-Path -LiteralPath $screenshotRoot -PathType Container)) {
        $violations.Add('current screenshot directory is missing: screenshots/0.5')
    }
    else {
        foreach ($screenshot in Get-ChildItem -LiteralPath $screenshotRoot -File -Filter '*.png') {
            $expectedReference = "../screenshots/0.5/$($screenshot.Name)"
            if (-not $guideContent.Contains($expectedReference)) {
                $violations.Add("operator guide does not reference current screenshot: $expectedReference")
            }
        }
    }
}
if (-not (Test-Path -LiteralPath $applicationReadmePath -PathType Leaf) -or
    -not [IO.File]::ReadAllText($applicationReadmePath).Contains('(guide/README.md)')) {
    $violations.Add('application README does not link to guide/README.md')
}
$resultRoot = Join-Path $resolvedRoot 'eval/results'
if (Test-Path -LiteralPath $resultRoot) {
    foreach ($resultFile in Get-ChildItem -LiteralPath $resultRoot -Force -File) {
        if ($resultFile.Name -ne '.gitkeep') {
            $violations.Add("historical eval result is present: eval/results/$($resultFile.Name)")
        }
    }
}
$files = Get-ChildItem -LiteralPath $resolvedRoot -Recurse -Force -File | Where-Object {
    $relative = $_.FullName.Substring($resolvedRoot.Length).TrimStart([char[]]'\/')
    if ($ignoredPath.IsMatch($relative)) {
        return $false
    }
    return $textExtensions.Contains($_.Extension) -or $textNames.Contains($_.Name)
}

foreach ($file in $files) {
    $relative = $file.FullName.Substring($resolvedRoot.Length).TrimStart([char[]]'\/')
    if ($legacyTokenPattern.IsMatch($relative) -or $agentContextPattern.IsMatch($relative) -or
        $forbiddenScript.IsMatch($relative) -or $forbiddenMarks.IsMatch($relative)) {
        $violations.Add("path: $relative")
    }
    $lineNumber = 0
    foreach ($line in [IO.File]::ReadLines($file.FullName)) {
        $lineNumber++
        if ($legacyTokenPattern.IsMatch($line) -or $agentContextPattern.IsMatch($line) -or
            $forbiddenScript.IsMatch($line) -or $forbiddenMarks.IsMatch($line)) {
            $violations.Add("text: ${relative}:$lineNumber")
        }
    }
}

if ($violations.Count -gt 0) {
    Write-Error ("Language-clean verification failed:`n" + ($violations -join "`n"))
    exit 1
}

Write-Host "GitHub-clean source verification passed ($($files.Count) text files checked)."
