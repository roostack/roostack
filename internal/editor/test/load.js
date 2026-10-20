// Loads the editor's scripts into Node the way a page does: as plain
// scripts in one global scope (vm.runInThisContext), in the page's order.
"use strict";
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = path.join(__dirname, "..", "static");
const order = ["yaml-2.9.1.min.js", "schema.js", "examples.js", "check.js", "compose.js", "suggest.js"];
for (const name of order) {
  vm.runInThisContext(fs.readFileSync(path.join(dir, name), "utf8"), { filename: name });
}
module.exports = globalThis.RoostackEditor;
