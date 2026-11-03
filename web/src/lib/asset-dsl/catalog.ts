export type AssetDslOperatorFamily = "text" | "exact" | "number" | "id";
type DslOperator = { op: string; desc: string };
export type AssetDslField = { name: string; desc: string; ops: DslOperator[] };

// Completion vocabulary only. Server parsing and scope checks stay authoritative.
// Construct fresh arrays/entries per field, retaining the original alias boundary.
function operatorsFor(family: AssetDslOperatorFamily): DslOperator[] {
  const textual = family === "text" || family === "exact";
  const choices: DslOperator[] = [];
  if (family === "text") choices.push({ op: "=", desc: "Fuzzy match" });
  choices.push({ op: "==", desc: textual ? "Exact match" : "Equals" });
  if (family !== "id") choices.push({ op: "!=", desc: textual ? "Exclude" : "Not equal" });
  if (family === "number")
    choices.push(
      { op: ">", desc: "Greater than" },
      { op: ">=", desc: "Greater than or equal" },
      { op: "<", desc: "Less than" },
      { op: "<=", desc: "Less than or equal" },
    );
  return choices;
}

function field(name: string, desc: string, family: AssetDslOperatorFamily): AssetDslField {
  return { name, desc, ops: operatorsFor(family) };
}

export const assetDslFields: AssetDslField[] = [
  field("domain", "Domain (root/subdomain/service domain)", "text"),
  field("ip", "IPv4/IPv6 address", "text"),
  field("url", "Full URL (service/endpoint)", "text"),
  field("root_domain", "Root domain", "text"),
  field("page_title", "Page title (HTTP service)", "text"),
  field("icp", "ICP filing number", "text"),
  field("service_name", "Service name (non-HTTP service)", "text"),
  field("app_name", "App name", "text"),
  field("bundle_id", "App Bundle ID", "text"),
  field("category", "App category", "text"),
  field("app_icp", "App ICP filing", "text"),
  field("method", "HTTP method GET/POST/PUT/…", "exact"),
  field("service_type", "Service type: http | other", "exact"),
  field("record_type", "DNS record type A/CNAME/MX/…", "exact"),
  field("technology", "Technology fingerprint (array field)", "text"),
  field("port", "Port number (integer)", "number"),
  field("status_code", "HTTP status code (integer)", "number"),
  field("company_id", "Owning company ID (integer)", "id"),
  field("task_id", "Source task ID (integer)", "id"),
];

export const assetDslLogic = [
  {
    label: "AND",
    desc: "And (both conditions match)",
  },
  {
    label: "OR",
    desc: "Or (either one matches)",
  },
];
