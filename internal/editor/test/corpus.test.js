// The editor's checks say exactly what pkgspec says, on every package of
// the corpus (testdata/corpus.json, written by Go's TestCorpusFile from
// the test packages, each changed one place at a time). Run with:
//   node --test internal/editor/test/
"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const R = require("./load.js");

const root = path.join(__dirname, "..");
const cases = JSON.parse(fs.readFileSync(path.join(root, "testdata", "corpus.json"), "utf8"));
// The bases are the test packages the editor offers as examples
// (examples.js, generated from the same files by schema_test.go).
const baseText = Object.fromEntries(R.examples.map((e) => [e.name, e.text]));

// applyEdits makes a case's package from its base, as corpus_test.go does.
function applyEdits(text, edits) {
  const doc = YAML.parseDocument(text, { merge: true });
  for (const e of edits || []) {
    if (e.delete) {
      const parent = doc.getIn(e.path.slice(0, -1), true);
      if (parent && (YAML.isMap(parent) || YAML.isSeq(parent))) parent.delete(e.path[e.path.length - 1]);
      continue;
    }
    const value = YAML.parseDocument(e.yaml).contents;
    // Make what's missing on the way, a list where the next step is a
    // number, as the Go side does.
    for (let i = 1; i < e.path.length; i++) {
      const at = e.path.slice(0, i);
      const node = doc.getIn(at, true);
      if (!node || !(YAML.isMap(node) || YAML.isSeq(node))) {
        doc.setIn(at, typeof e.path[i] === "number" ? doc.createNode([]) : doc.createNode({}));
      }
    }
    const last = e.path[e.path.length - 1];
    const parent = doc.getIn(e.path.slice(0, -1), true) || doc.contents;
    if (typeof last === "number" && YAML.isSeq(parent) && last >= parent.items.length) parent.items.push(value);
    else doc.setIn(e.path, value === null ? null : value);
  }
  return doc.toString();
}

function sorted(xs) {
  return [...xs].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
}

test("the editor's checks agree with pkgspec on the corpus", () => {
  const failures = [];
  for (const [i, c] of cases.entries()) {
    const text = c.base ? applyEdits(baseText[c.base], c.edits) : c.text || "";
    const got = R.check.run(text);
    const decoded = !got.problems.some((p) => p.source === "decode" || p.source === "yaml");
    const label = `case ${i} (${c.base || "by hand"} ${JSON.stringify(c.edits || (c.text || "").slice(0, 40))})`;
    if (!!c.decode !== !decoded) {
      failures.push(`${label}: Go ${c.decode ? "couldn't" : "could"} decode it; the editor ${decoded ? "could" : "couldn't"}: ${JSON.stringify(got.problems.slice(0, 2))}`);
      continue;
    }
    if (c.decode) continue;
    const validate = sorted(got.problems.filter((p) => p.kind === "validation" && p.source === "validation").map((p) => p.message));
    try {
      assert.deepEqual(validate, c.validate || []);
    } catch {
      failures.push(`${label}: Validate\n  Go:     ${JSON.stringify(c.validate || [])}\n  editor: ${JSON.stringify(validate)}`);
      continue;
    }
    if (validate.length) continue;
    const policy = sorted(got.problems.filter((p) => p.kind === "registry").map((p) => p.message));
    const risks = sorted(R.check.run(text, { local: true }).problems.filter((p) => p.kind === "registry").map((p) => p.message));
    try {
      assert.deepEqual(policy, c.policy || []);
      assert.deepEqual(risks, c.risks || []);
    } catch {
      failures.push(`${label}: Policy\n  Go:     ${JSON.stringify(c.policy || [])} / ${JSON.stringify(c.risks || [])}\n  editor: ${JSON.stringify(policy)} / ${JSON.stringify(risks)}`);
    }
  }
  if (failures.length) assert.fail(`${failures.length} of ${cases.length} cases differ:\n\n` + failures.slice(0, 40).join("\n\n"));
});

// Every value test the policy names is one the editor has.
test("the editor has every Quadlet value test", () => {
  assert.deepEqual(sorted(Object.keys(R.check.valueTests)), R.schema.valueTests);
});
