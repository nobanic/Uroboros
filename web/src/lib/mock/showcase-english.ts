const HAN_TEXT = /\p{Script=Han}/u;
const MOJIBAKE_TEXT = /[ÃÂäåæçèéïð]|\uFFFD/u;

const FIELD_FALLBACKS: Record<string, string> = {
  app_name: "Uroboros Lab application",
  content: "Demo operator note: review the linked evidence and continue the verified assessment workflow.",
  description: "Security assessment record for the uroboros.lab demonstration environment.",
  detail: "Reproducible evidence was captured and attached to this demonstration record.",
  display: "uroboros.lab demo asset",
  evidence:
    "A reproducible request confirmed the observed security behavior in the uroboros.lab demonstration environment.",
  goal: "Validate the exposed attack surface and capture reproducible security impact.",
  label: "uroboros.lab demo record",
  message: "Demo workflow update recorded.",
  name: "uroboros.lab security record",
  output: "The mock tool completed successfully and returned demonstration evidence.",
  page_title: "Uroboros Lab application",
  prompt: "Review the available evidence and choose the safest next action.",
  reason: "Recorded by the demonstration policy.",
  summary: "Verified security evidence captured during the uroboros.lab demonstration assessment.",
  text: "Demo assessment update.",
  title: "uroboros.lab security record",
};

function showcaseTenant(value: string): string {
  return value
    .replace(/Acme@/g, "UroborosLab@")
    .replace(/acme_prod/gi, "uroboros_lab_prod")
    .replace(/acme\.(?:com|test)/gi, "uroboros.lab")
    .replace(/Acme Corp(?:oration)?/g, "Uroboros Lab")
    .replace(/\bAcme\b/g, "Uroboros Lab")
    .replace(/\bacme\b/g, "uroboros.lab");
}

function hasNonEnglishFixtureText(value: string): boolean {
  return HAN_TEXT.test(value) || MOJIBAKE_TEXT.test(value);
}

function usefulAsciiTokens(value: string): string[] {
  return (value.match(/(?:https?:\/\/)?(?:[A-Za-z0-9][A-Za-z0-9_.:@/-]*[A-Za-z0-9]|[A-Za-z][A-Za-z0-9_-]{2,})/g) ?? [])
    .filter((token) => !/^(const|false|null|true|undefined)$/i.test(token))
    .slice(0, 5);
}

function fallbackText(value: string, field: string): string {
  const compact = ["label", "name", "page_title", "title"].includes(field);
  const contextual = (() => {
    if (/DC01|secretsdump|Domain Admin/i.test(value)) {
      return compact
        ? "Domain controller compromise validated"
        : "Domain controller access was validated and the resulting privilege evidence was preserved for review.";
    }
    if (/Fastjson|JNDI|JdbcRowSet|deserial/i.test(value)) {
      return compact
        ? "Fastjson remote execution path"
        : "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.";
    }
    if (/Jenkins|Groovy|\/script/i.test(value)) {
      return compact
        ? "Jenkins execution path validated"
        : "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.";
    }
    if (/IDOR|orders|ownership/i.test(value)) {
      return compact
        ? "Order authorization boundary"
        : "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.";
    }
    if (/SQL|MSSQL|sqlmap|CONVERT/i.test(value)) {
      return compact
        ? "SQL injection evidence"
        : "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.";
    }
    if (/sudo|www-data|root|shell/i.test(value)) {
      return compact
        ? "Privilege escalation evidence"
        : "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.";
    }
    if (/JWT|HS256|John/i.test(value)) {
      return compact
        ? "JWT signing weakness"
        : "Offline verification recovered the weak signing secret and confirmed that arbitrary identity claims could be forged.";
    }
    if (/\.git|git-dumper|source/i.test(value)) {
      return compact
        ? "Exposed source history"
        : "The exposed source repository was recovered and linked to configuration evidence in the assessment record.";
    }
    return "";
  })();
  if (contextual) return contextual;

  const tokens = usefulAsciiTokens(value);
  const context = tokens.length > 0 ? ` Context: ${tokens.join(", ")}.` : "";
  const fallback = FIELD_FALLBACKS[field] ?? "uroboros.lab demonstration record.";
  return `${fallback}${context}`;
}

function sanitizeString(value: string, field: string): string {
  if (!hasNonEnglishFixtureText(value)) return showcaseTenant(value);

  const trimmed = value.trim();
  if ((trimmed.startsWith("{") && trimmed.endsWith("}")) || (trimmed.startsWith("[") && trimmed.endsWith("]"))) {
    try {
      const parsed = JSON.parse(value) as unknown;
      return showcaseTenant(JSON.stringify(showcaseEnglish(parsed)));
    } catch {
      // Some evidence bodies resemble JSON but intentionally contain excerpts.
    }
  }

  return showcaseTenant(fallbackText(value, field));
}

/**
 * The mock dataset predates the English showcase and contains a mix of Chinese
 * strings and legacy mojibake. Mock responses pass through this final boundary
 * so no old fixture can leak untranslated copy into a demo route. Known,
 * prominent records are authored directly in English; this is the fail-safe for
 * nested evidence, archived records, and less frequently visited panels.
 */
export function showcaseEnglish<T>(value: T, field = "record"): T {
  if (typeof value === "string") return sanitizeString(value, field) as T;
  if (Array.isArray(value)) return value.map((item) => showcaseEnglish(item, field)) as T;
  if (!value || typeof value !== "object") return value;

  const source = value as Record<string, unknown>;
  const translated: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(source)) translated[key] = showcaseEnglish(item, key);
  return translated as T;
}
