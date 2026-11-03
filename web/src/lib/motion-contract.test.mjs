import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const css = readFileSync(new URL("../app/globals.css", import.meta.url), "utf8");

function ruleBody(source, marker) {
  const markerIndex = source.indexOf(marker);
  assert.notEqual(markerIndex, -1, `missing CSS rule: ${marker}`);

  const openIndex = source.indexOf("{", markerIndex);
  assert.notEqual(openIndex, -1, `missing opening brace for: ${marker}`);

  let depth = 0;
  for (let index = openIndex; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] !== "}") continue;
    depth -= 1;
    if (depth === 0) return source.slice(openIndex + 1, index);
  }

  assert.fail(`missing closing brace for: ${marker}`);
}

test("reduced motion keeps operational keyframe animations running", () => {
  const reducedMotion = ruleBody(css, "@media (prefers-reduced-motion: reduce)");

  assert.match(reducedMotion, /scroll-behavior:\s*auto\s*!important/);
  assert.match(reducedMotion, /transition-duration:\s*0\.01ms\s*!important/);
  assert.doesNotMatch(reducedMotion, /animation-duration\s*:/);
  assert.doesNotMatch(reducedMotion, /animation-iteration-count\s*:/);
});
