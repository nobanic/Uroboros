#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)"

usage() {
    cat <<'EOF'
Usage: verify-language.sh [--root PATH]
EOF
}

while (( $# > 0 )); do
    case "$1" in
        --root)
            (( $# >= 2 )) || { usage >&2; exit 2; }
            ROOT="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            printf 'Unknown argument: %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

[[ -d "$ROOT" ]] || { printf 'Source root does not exist: %s\n' "$ROOT" >&2; exit 1; }
ROOT="$(CDPATH= cd -- "$ROOT" && pwd -P)"

if ! printf 'x\n' | grep -Pq 'x' 2>/dev/null; then
    printf 'GNU grep with PCRE support is required for source verification.\n' >&2
    exit 1
fi

legacy_1='aut''umn'
legacy_2='art''ex'
legacy_3='art''umn'
legacy_4='scope''sentry'
agent_1='co''dex'
agent_2='clau''de-code'
agent_3='clau''de code'
token_pattern="(?i)(?<![A-Za-z0-9])(?:${legacy_1}|${legacy_2}|${legacy_3}|${legacy_4}|${agent_1}|${agent_2}|${agent_3})(?![A-Za-z0-9])"
unicode_pattern='[\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}\x{3000}-\x{303F}\x{FE30}-\x{FE4F}\x{FF01}-\x{FF60}]'
forbidden_pattern="(?:${token_pattern})|(?:${unicode_pattern})"

agent_upper="$(printf '%s' "$agent_1" | tr '[:lower:]' '[:upper:]')"
other_agent='clau''de'
other_agent_upper="$(printf '%s' "$other_agent" | tr '[:lower:]' '[:upper:]')"
removed_paths=(
    'CHANGELOG.md'
    'CONSOLE_GUIDE_CZ.md'
    'handoff'
    ".${other_agent}"
    ".${agent_1}"
    '.superpowers'
    "${other_agent_upper}.md"
    "${agent_upper}.md"
    'AGENTS.md'
    'sidequestion/CONTEXT_BUDGET.md'
    'sidequestion/VALIDATION.md'
    'sidequestion/validation-2026-09-10.json'
)

violations=()
for relative in "${removed_paths[@]}"; do
    if [[ -e "$ROOT/$relative" ]]; then
        violations+=("removed path is present: $relative")
    fi
done

guide="$ROOT/guide/README.md"
readme="$ROOT/README.md"
screenshot_root="$ROOT/screenshots/0.5"
if [[ ! -f "$guide" ]]; then
    violations+=("required operator guide is missing: guide/README.md")
elif [[ ! -d "$screenshot_root" ]]; then
    violations+=("current screenshot directory is missing: screenshots/0.5")
else
    while IFS= read -r -d '' screenshot; do
        expected="../screenshots/0.5/$(basename -- "$screenshot")"
        if ! grep -Fq -- "$expected" "$guide"; then
            violations+=("operator guide does not reference current screenshot: $expected")
        fi
    done < <(find "$screenshot_root" -maxdepth 1 -type f -name '*.png' -print0)
fi

if [[ ! -f "$readme" ]] || ! grep -Fq -- '(guide/README.md)' "$readme"; then
    violations+=("application README does not link to guide/README.md")
fi

if [[ -d "$ROOT/eval/results" ]]; then
    while IFS= read -r -d '' result_file; do
        if [[ "$(basename -- "$result_file")" != '.gitkeep' ]]; then
            violations+=("historical eval result is present: eval/results/$(basename -- "$result_file")")
        fi
    done < <(find "$ROOT/eval/results" -maxdepth 1 -type f -print0)
fi

is_text_file() {
    local name="$1"
    case "$name" in
        Dockerfile|LICENSE|Makefile|NOTICE|torrc|.dockerignore|.env|.gitignore|\
        *.cjs|*.css|*.go|*.html|*.js|*.json|*.jsx|*.md|*.mjs|*.mod|*.ps1|\
        *.py|*.sh|*.sql|*.sum|*.toml|*.ts|*.tsx|*.txt|*.yaml|*.yml)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

file_count=0
text_files=()
ascii_path_pattern="(^|[^A-Za-z0-9])(${legacy_1}|${legacy_2}|${legacy_3}|${legacy_4}|${agent_1}|${agent_2}|${agent_3})($|[^A-Za-z0-9])"
non_ascii_glob='*[! -~]*'
shopt -s nocasematch
while IFS= read -r -d '' file; do
    name="${file##*/}"
    is_text_file "$name" || continue
    relative="${file#"$ROOT"/}"
    file_count=$((file_count + 1))
    text_files+=("$file")

    if [[ "$relative" =~ $ascii_path_pattern ]]; then
        violations+=("path: $relative")
    fi
    if [[ "$relative" == $non_ascii_glob ]] && printf '%s\n' "$relative" | grep -Pq "$unicode_pattern"; then
        violations+=("path: $relative")
    fi
done < <(
    find "$ROOT" \
        \( -type d \( -name .git -o -name node_modules -o -name .next -o -name out -o -name dist -o -name runtime-images -o -name UroborOS \) -prune \) \
        -o -type f -print0
)
shopt -u nocasematch

if (( ${#text_files[@]} > 0 )); then
    while IFS= read -r -d '' file; do
        relative="${file#"$ROOT"/}"
        match="$(grep -Pn -m1 "$forbidden_pattern" "$file" 2>/dev/null || true)"
        violations+=("text: ${relative}:${match%%:*}")
    done < <(
        for (( offset = 0; offset < ${#text_files[@]}; offset += 100 )); do
            batch=("${text_files[@]:offset:100}")
            grep -PIlZ -- "$forbidden_pattern" "${batch[@]}" || true
        done
    )
fi

if (( ${#violations[@]} > 0 )); then
    printf 'Language-clean verification failed:\n' >&2
    printf '%s\n' "${violations[@]}" >&2
    exit 1
fi

printf 'GitHub-clean source verification passed (%d text files checked).\n' "$file_count"
