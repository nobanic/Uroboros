import fs from "node:fs";
import path from "node:path";

const outputRoot = path.resolve(process.argv[2] ?? "out");
const forbiddenPattern = /[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3000-\u303f\ufe30-\ufe4f\uff01-\uff60]/gu;
const legacyTokens = ["aut" + "umn", "art" + "ex", "art" + "umn", "scope" + "sentry"];
const legacyTokenPattern = new RegExp(`(?<![A-Za-z0-9])(?:${legacyTokens.join("|")})(?![A-Za-z0-9])`, "i");
const agentContextTokens = ["cod" + "ex", "claude" + "-code", "claude" + " code"];
const agentContextPattern = new RegExp(`(?<![A-Za-z0-9])(?:${agentContextTokens.join("|")})(?![A-Za-z0-9])`, "i");
const escapableExtensions = new Set([".js", ".json", ".map"]);
const textExtensions = new Set([".css", ".html", ".js", ".json", ".map", ".svg", ".txt", ".xml"]);

function filesBelow(directory) {
  const result = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const fullPath = path.join(directory, entry.name);
    if (entry.isDirectory()) result.push(...filesBelow(fullPath));
    else if (entry.isFile()) result.push(fullPath);
  }
  return result;
}

function escapedCodePoint(value) {
  return `\\u${value.codePointAt(0).toString(16).padStart(4, "0")}`;
}

const violations = [];
let rewritten = 0;
for (const filename of filesBelow(outputRoot)) {
  const relative = path.relative(outputRoot, filename);
  if (legacyTokenPattern.test(relative) || agentContextPattern.test(relative)) violations.push(`path: ${relative}`);

  const extension = path.extname(filename).toLowerCase();
  if (!textExtensions.has(extension)) continue;
  let content = fs.readFileSync(filename, "utf8");
  if (escapableExtensions.has(extension) && forbiddenPattern.test(content)) {
    forbiddenPattern.lastIndex = 0;
    content = content.replace(forbiddenPattern, escapedCodePoint);
    fs.writeFileSync(filename, content, "utf8");
    rewritten++;
  }
  forbiddenPattern.lastIndex = 0;
  if (forbiddenPattern.test(content)) violations.push(`text: ${relative}`);
  forbiddenPattern.lastIndex = 0;
  if (legacyTokenPattern.test(content)) violations.push(`text: ${relative}`);
  if (agentContextPattern.test(content)) violations.push(`agent context: ${relative}`);
}

if (violations.length > 0) {
  throw new Error(`Static-output language verification failed:\n${violations.join("\n")}`);
}

process.stdout.write(`Static-output language verification passed (${rewritten} generated files escaped).\n`);
