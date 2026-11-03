import type { Edge, TaskNode } from "../types";

export type NodeMetadata = Omit<TaskNode, "id" | "type" | "payload" | "origin"> & { display_title?: string };
export type RecordedNode = Readonly<{ id: string }>;

/** Seed graph ownership is explicit; edges bind registered nodes without imposing a DAG. */
export class RecordedGraph {
  private readonly nodes: TaskNode[] = [];
  private readonly edges: Edge[] = [];
  private readonly references = new Map<string, RecordedNode>();

  origin(actor: string) {
    if (!actor) throw new Error("A recorded graph needs a named origin");
    return {
      node: (
        type: TaskNode["type"],
        id: string,
        payload: Record<string, string>,
        metadata: NodeMetadata,
      ): RecordedNode => {
        if (!id || this.references.has(id)) throw new Error(`Duplicate or empty graph identity: ${id}`);
        if (!Number.isSafeInteger(metadata.priority) || metadata.priority < 0 || metadata.priority > 10) {
          throw new Error("Recorded graph priority must be an integer from zero to ten");
        }
        if (!Number.isFinite(Date.parse(metadata.ts))) throw new Error("A recorded graph needs a valid timestamp");
        const { priority, state, ts, ...context } = metadata;
        this.nodes.push({ id, type, payload: JSON.stringify(payload), priority, state, origin: actor, ts, ...context });
        const reference = Object.freeze({ id });
        this.references.set(id, reference);
        return reference;
      },
    };
  }

  link(source: RecordedNode, destination: RecordedNode, relation: Edge["rel"]) {
    if (this.references.get(source.id) !== source || this.references.get(destination.id) !== destination) {
      throw new Error("Recorded relations must reference nodes registered in this graph");
    }
    this.edges.push({ src: source.id, dst: destination.id, rel: relation });
  }

  connect(sourceID: string, relation: Edge["rel"], destinationID: string) {
    const source = this.references.get(sourceID), destination = this.references.get(destinationID);
    if (!source || !destination) throw new Error("A recorded connection needs two registered node IDs");
    this.link(source, destination, relation);
  }

  snapshot(): { nodes: TaskNode[]; edges: Edge[] } {
    return { nodes: this.nodes, edges: this.edges };
  }
}
