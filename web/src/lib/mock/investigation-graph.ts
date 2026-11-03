import type { Edge, TaskNode } from "../types";
import { RecordedGraph, type NodeMetadata } from "./recorded-graph";

type StoryNode = { kind: TaskNode["type"]; id: string; title: string; payload: Record<string, string>; metadata: NodeMetadata };

function record(kind: TaskNode["type"], id: string, title: string, payload: Record<string, string>, metadata: NodeMetadata): StoryNode {
  return { kind, id, title, payload, metadata };
}

/** Display captions accompany the original evidence; named relations preserve its complete ordered lineage. */
export function createInvestigationGraph(): { nodes: TaskNode[]; edges: Edge[] } {
  const graph = new RecordedGraph();
  function stage(actor: string, records: StoryNode[]) {
    const origin = graph.origin(actor);
    for (const row of records) origin.node(row.kind, row.id, row.payload, { ...row.metadata, display_title: row.title });
  }

  stage("system", [
    record("fact", "root", "External perimeter: acme.com",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: acme.com." },
      { "priority": 0, "state": "origin", "ts": "2026-07-24T09:12:00Z" }),
  ]);

  stage("goals", [
    record("goal", "g1", "Validate administrative access",
      { "text": "Demo assessment update. Context: acme.com." },
      { "priority": 9, "state": "met", "ts": "2026-07-24T09:13:00Z" }),
    record("goal", "g2", "Assess application data exposure",
      { "text": "Demo assessment update." },
      { "priority": 8, "state": "open", "ts": "2026-07-24T09:13:00Z" }),
    record("goal", "g3", "Establish the DMZ foothold",
      { "text": "Demo assessment update. Context: DMZ." },
      { "priority": 9, "state": "met", "ts": "2026-07-25T14:10:00Z" }),
    record("goal", "g4", "Validate domain controller access",
      { "text": "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
      { "priority": 10, "state": "met", "ts": "2026-07-25T20:00:00Z" }),
  ]);

  stage("planner", [
    record("intent", "i1", "Map the external perimeter",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: acme.com." },
      { "priority": 6, "state": "done", "ts": "2026-07-24T09:20:00Z" }),
    record("intent", "i2", "Check the administrative sign-in",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin." },
      { "priority": 9, "state": "done", "ts": "2026-07-26T03:30:00Z" }),
    record("intent", "i3", "Continue admin verification",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment." },
      { "priority": 8, "state": "running", "ts": "2026-07-26T03:55:00Z" }),
    record("intent", "i4", "Verify the search injection path",
      { "summary": "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary." },
      { "priority": 8, "state": "done", "ts": "2026-07-25T22:00:00Z" }),
    record("intent", "i5", "Check cross-account order access",
      { "summary": "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check." },
      { "priority": 8, "state": "running", "ts": "2026-07-26T02:30:00Z" }),
    record("intent", "ig", "Recover exposed source history",
      { "summary": "The exposed source repository was recovered and linked to configuration evidence in the assessment record." },
      { "priority": 7, "state": "done", "ts": "2026-07-25T20:20:00Z" }),
    record("intent", "i6", "Review the storefront attack surface",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: shop.acme.com, CVE." },
      { "priority": 7, "state": "done", "ts": "2026-07-25T14:20:00Z" }),
    record("intent", "i7", "Validate the remote shell",
      { "summary": "The worker validated the local privilege boundary and preserved the controlled shell evidence for review." },
      { "priority": 9, "state": "done", "ts": "2026-07-25T15:10:00Z" }),
    record("intent", "i8", "Test local privilege boundaries",
      { "summary": "The worker validated the local privilege boundary and preserved the controlled shell evidence for review." },
      { "priority": 8, "state": "done", "ts": "2026-07-25T16:00:00Z" }),
    record("intent", "i9", "Trace the internal network pivot",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: pivot." },
      { "priority": 8, "state": "done", "ts": "2026-07-25T20:30:00Z" }),
    record("intent", "i10", "Verify recovered database access",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment." },
      { "priority": 8, "state": "running", "ts": "2026-07-26T03:40:00Z" }),
    record("intent", "i11", "Test the Jenkins script console",
      { "summary": "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence." },
      { "priority": 9, "state": "done", "ts": "2026-07-25T21:30:00Z" }),
    record("intent", "i12", "Review Kerberos service tickets",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: Kerberoasting." },
      { "priority": 8, "state": "done", "ts": "2026-07-25T22:40:00Z" }),
    record("intent", "i13", "Validate the domain access path",
      { "summary": "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
      { "priority": 10, "state": "done", "ts": "2026-07-25T23:50:00Z" }),
  ]);

  stage("work#1", [
    record("fact", "fa1", "Administrative UI fingerprint",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin.acme.com, Element-UI." },
      { "priority": 0, "state": "open", "ts": "2026-07-24T09:40:00Z" }),
  ]);

  stage("work#4", [
    record("fact", "fa2", "Search injection response",
      { "summary": "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T22:05:00Z" }),
  ]);

  stage("workG", [
    record("fact", "faGit", "Recovered repository evidence",
      { "summary": "The exposed source repository was recovered and linked to configuration evidence in the assessment record." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T20:30:00Z" }),
    record("fact", "faCreds", "Configuration credential evidence",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: config.php, DB, sa, Acme@2021." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T20:35:00Z" }),
  ]);

  stage("work#6", [
    record("fact", "fa3", "Fastjson callback evidence",
      { "summary": "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T14:40:00Z" }),
  ]);

  stage("work#7", [
    record("fact", "fa4", "Remote shell evidence",
      { "summary": "The worker validated the local privilege boundary and preserved the controlled shell evidence for review." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T15:40:00Z" }),
  ]);

  stage("work#8", [
    record("fact", "fa5", "Local privilege evidence",
      { "summary": "The worker validated the local privilege boundary and preserved the controlled shell evidence for review." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T16:20:00Z" }),
  ]);

  stage("work#9", [
    record("fact", "fa6", "DMZ internal network route",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: 10.10.10.0/24, DMZ." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T20:40:00Z" }),
    record("fact", "fa7", "Internal domain context",
      { "summary": "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T21:00:00Z" }),
  ]);

  stage("work#11", [
    record("fact", "fa8", "Jenkins execution evidence",
      { "summary": "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T21:45:00Z" }),
    record("fact", "fa9", "Jenkins follow-up artifacts",
      { "summary": "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T22:10:00Z" }),
  ]);

  stage("work#12", [
    record("fact", "fa10", "Service ticket evidence",
      { "summary": "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary." },
      { "priority": 0, "state": "open", "ts": "2026-07-25T23:20:00Z" }),
  ]);

  stage("work#2", [
    record("finding", "fi1", "Weak administrative credentials",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin/admin123." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-26T03:50:00Z" }),
  ]);

  stage("work#4", [
    record("finding", "fi2", "Search SQL injection",
      { "summary": "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-26T01:20:00Z" }),
  ]);

  stage("work#5", [
    record("finding", "fi3", "Cross-account order disclosure",
      { "summary": "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-26T02:44:00Z" }),
  ]);

  stage("workG", [
    record("finding", "fiSrc", "Database credentials in source",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: sa/Acme@2021." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-25T20:36:00Z" }),
  ]);

  stage("work#7", [
    record("finding", "fiRce", "Fastjson remote code execution",
      { "summary": "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-25T15:40:00Z" }),
  ]);

  stage("work#8", [
    record("finding", "fiPriv", "Local privilege escalation",
      { "summary": "The worker validated the local privilege boundary and preserved the controlled shell evidence for review." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-25T16:20:00Z" }),
  ]);

  stage("work#11", [
    record("finding", "fiJenkins", "Unauthenticated Jenkins execution",
      { "summary": "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-25T21:50:00Z" }),
  ]);

  stage("work#12", [
    record("finding", "fiKerb", "Kerberos service account exposure",
      { "summary": "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-25T23:20:00Z" }),
  ]);

  stage("work#13", [
    record("finding", "fiDC", "Domain controller compromise",
      { "summary": "Domain controller access was validated and the resulting privilege evidence was preserved for review." },
      { "priority": 0, "state": "confirmed", "ts": "2026-07-26T00:20:00Z" }),
  ]);

  stage("mainagent", [
    record("hint", "h1", "Review Element-UI sign-in",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: Element-UI." },
      { "priority": 5, "state": "consumed", "ts": "2026-07-26T03:20:00Z" }),
    record("hint", "h2", "Inspect the Jenkins surface",
      { "summary": "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence." },
      { "priority": 6, "state": "consumed", "ts": "2026-07-25T21:20:00Z" }),
    record("hint", "h3", "Follow the database lead",
      { "summary": "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: DB." },
      { "priority": 6, "state": "consumed", "ts": "2026-07-25T23:10:00Z" }),
  ]);

  const relations: [source: string, relation: Edge["rel"], destination: string][] = [
    ["root", "spawns", "g1"],
    ["root", "spawns", "g2"],
    ["root", "spawns", "g3"],
    ["root", "spawns", "g4"],
    ["g1", "spawns", "i1"],
    ["g1", "spawns", "i2"],
    ["g1", "spawns", "i3"],
    ["g2", "spawns", "i4"],
    ["g2", "spawns", "i5"],
    ["g2", "spawns", "ig"],
    ["g3", "spawns", "i6"],
    ["g3", "spawns", "i7"],
    ["g3", "spawns", "i8"],
    ["g4", "spawns", "i9"],
    ["g4", "spawns", "i10"],
    ["g4", "spawns", "i11"],
    ["g4", "spawns", "i12"],
    ["g4", "spawns", "i13"],
    ["i1", "yields", "fa1"],
    ["i4", "yields", "fa2"],
    ["i2", "yields", "fi1"],
    ["i4", "yields", "fi2"],
    ["i5", "yields", "fi3"],
    ["ig", "yields", "faGit"],
    ["ig", "yields", "faCreds"],
    ["ig", "yields", "fiSrc"],
    ["i6", "yields", "fa3"],
    ["i7", "yields", "fa4"],
    ["i7", "yields", "fiRce"],
    ["i8", "yields", "fa5"],
    ["i8", "yields", "fiPriv"],
    ["i9", "yields", "fa6"],
    ["i9", "yields", "fa7"],
    ["i11", "yields", "fa8"],
    ["i11", "yields", "fa9"],
    ["i11", "yields", "fiJenkins"],
    ["i12", "yields", "fa10"],
    ["i12", "yields", "fiKerb"],
    ["i13", "yields", "fiDC"],
    ["fa3", "derived_from", "i7"],
    ["fa4", "derived_from", "i8"],
    ["fa4", "derived_from", "i9"],
    ["faCreds", "derived_from", "i10"],
    ["fa7", "derived_from", "i11"],
    ["fa7", "derived_from", "i12"],
    ["fa9", "derived_from", "i13"],
    ["fa10", "derived_from", "i13"],
    ["h1", "derived_from", "i2"],
    ["h2", "derived_from", "i11"],
    ["h3", "derived_from", "i10"],
    ["fi1", "proves", "g1"],
    ["fiRce", "proves", "g3"],
    ["fiDC", "proves", "g4"],
  ];
  for (const [source, relation, destination] of relations) graph.connect(source, relation, destination);
  return graph.snapshot();
}
