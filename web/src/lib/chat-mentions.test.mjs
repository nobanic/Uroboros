import { activeMention, mentionSearch, mentionToken, selectedMentions } from "./chat-mentions.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("mention trigger supports labels and cursor placement without hijacking email", () => {
  assert.equal(activeMention("user@example.com", 16), null);
  const selected = "Selected @[Finding#1 X]";
  assert.equal(activeMention(selected, selected.length), null);
  assert.deepEqual(activeMention("Check @fin trailing", 10), {
    start: 6,
    end: 10,
    query: "fin",
  });
  assert.equal(activeMention("@fin\nnext", 9), null);
});

test("categories, aliases, IP and keyword search", () => {
  assert.equal(mentionSearch("").categories.length, 9);
  assert.equal(mentionSearch("finding").kind, "finding");
  assert.equal(mentionSearch("finding SQL injection").query, "SQL injection");
  assert.equal(mentionSearch("ip 192.0.2.1").kind, "ip");
  assert.equal(mentionSearch("endpoint GET /api").query, "GET /api");
  assert.equal(mentionSearch("acme.com").kind, "");
});

test("tokens roundtrip labels and removing one reference preserves its neighbors", () => {
  const first = mentionToken({ kind: "finding", id: 12, label: "SQL injection [verified]\nevidence" });
  const second = mentionToken({ kind: "ip", id: 13, label: "192.0.2.1" });
  const value = `Analyze ${first} and ${second}`;
  const selected = selectedMentions(value);
  assert.equal(selected.length, 2);
  assert.equal(selected[0].label, "Finding #12 · SQL injection (verified) evidence");
  const next = value.slice(0, selected[0].start) + value.slice(selected[0].start + selected[0].token.length);
  assert.equal(selectedMentions(next)[0].token, second);
});
