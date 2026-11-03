import { assetDslFields, assetDslLogic } from "./catalog";

export interface AssetDslSuggestion {
  kind: "field" | "operator" | "logic";
  label: string;
  desc: string;
  replaceStart: number;
  replaceEnd: number;
  insertText: string;
}

type SuggestionRange = { start: number; end: number };

function choice(
  kind: AssetDslSuggestion["kind"],
  label: string,
  desc: string,
  range: SuggestionRange,
): AssetDslSuggestion {
  return {
    kind,
    label,
    desc,
    replaceStart: range.start,
    replaceEnd: range.end,
    insertText: kind === "logic" ? `${label} ` : label,
  };
}

// This is the original cursor-oriented completion grammar, not the server's
// authoritative DSL parser. Keep its UTF-16 ranges and lexical precedence.
export function getAssetDslSuggestions(text: string, cursor: number): AssetDslSuggestion[] {
  const before = text.slice(0, cursor);
  const token = before.match(/([^\s()]*$)/)?.[1] ?? "";
  const range = { start: cursor - token.length, end: cursor };
  if (/^[a-z_]+(==|!=|>=|<=|=|>|<)/.test(token)) return [];

  const field = assetDslFields.find((candidate) => candidate.name === token.toLowerCase());
  if (field) {
    return field.ops.map(({ op, desc }) => choice("operator", `${field.name}${op}`, desc, range));
  }

  const preceding = before.slice(0, range.start).trimEnd();
  const afterExpression = preceding.length > 0 && !/\b(AND|OR)\s*$/i.test(preceding) && !preceding.endsWith("(");
  if (afterExpression && (!token || /^(a|an|and|o|or)$/i.test(token))) {
    return assetDslLogic
      .filter((operator) => !token || operator.label.startsWith(token.toUpperCase()))
      .map(({ label, desc }) => choice("logic", label, desc, range));
  }

  return assetDslFields
    .filter((candidate) => candidate.name.startsWith(token.toLowerCase()))
    .map(({ name, desc }) => choice("field", name, desc, range));
}

export function applyAssetDslSuggestion(
  text: string,
  suggestion: AssetDslSuggestion,
  cursor?: number,
): { text: string; cursor: number } {
  const atCaret =
    cursor !== undefined && suggestion.kind === "logic" && !text.slice(suggestion.replaceStart, suggestion.replaceEnd);
  const start = atCaret ? cursor : suggestion.replaceStart;
  const end = atCaret ? cursor : suggestion.replaceEnd;
  return {
    text: text.slice(0, start) + suggestion.insertText + text.slice(end),
    cursor: start + suggestion.insertText.length,
  };
}
