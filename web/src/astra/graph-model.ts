import { collection, isRow, type DataRow } from "./client";

export type InvestigationGraph = { nodes: DataRow[] | null; edges: DataRow[] | null };

export function payloadObject(value: unknown): DataRow {
  if (isRow(value)) return value;
  if (typeof value !== "string") return {};
  try {
    const parsed: unknown = JSON.parse(value);
    return isRow(parsed) ? parsed : { value: parsed };
  } catch {
    return { text: value };
  }
}

// The migration boundary accepts the existing wire DTO (type + JSON payload
// string) and the domain representation (kind + object) without losing evidence.
export function investigationGraph(value: unknown): InvestigationGraph {
  return {
    nodes: collection(value, ["nodes"]).map((node) => ({
      ...node,
      kind: node.kind ?? node.type,
      payload: payloadObject(node.payload),
    })),
    edges: collection(value, ["edges"]),
  };
}
