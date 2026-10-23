// Loads the editor's scripts into Node the way a page does: as plain
// scripts in one global scope (vm.runInThisContext), in the page's order
// (static/scripts.json). All but editor.js, the page itself, which needs
// a browser's document.
"use strict";
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = path.join(__dirname, "..", "static");
const { scripts } = JSON.parse(fs.readFileSync(path.join(dir, "scripts.json"), "utf8"));
for (const name of scripts.filter((s) => s !== "editor.js")) {
  vm.runInThisContext(fs.readFileSync(path.join(dir, name), "utf8"), { filename: name });
}
module.exports = globalThis.RoostackEditor;
