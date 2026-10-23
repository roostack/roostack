// The help's password gate (gate.js): node --test help/worker/*.test.js
//
// A copy of the test of Roostack Cloud's website's gate (site/test/gate.test.js
// in its repository), whose gate is the same: keep the two alike.
import { test } from "node:test";
import assert from "node:assert/strict";
import { gate, gated } from "./gate.js";

const env = { GATE: "password", SITE_PASSWORD: "s3cret pass" };
const req = (auth) => new Request("https://help.roostack.cloud/", auth ? { headers: { Authorization: auth } } : {});
const basic = (user, pw) => "Basic " + btoa(String.fromCharCode(...new TextEncoder().encode(`${user}:${pw}`)));

test("the right password, any user name, goes on", async () => {
  assert.equal(await gate(req(basic("anyone", "s3cret pass")), env), null);
  assert.equal(await gate(req(basic("", "s3cret pass")), env), null);
});

test("no, a wrong or a malformed password is asked for", async () => {
  for (const auth of [undefined, basic("a", "wrong"), basic("a", "s3cret pass "), "Basic !!!", "Bearer x", basic("a", "")]) {
    const res = await gate(req(auth), env);
    assert.equal(res.status, 401, String(auth));
    assert.match(res.headers.get("WWW-Authenticate"), /^Basic realm=/);
    assert.equal(res.headers.get("X-Robots-Tag"), "noindex, nofollow");
  }
});

test("on without a password set, nothing is served", async () => {
  const res = await gate(req(basic("a", "anything")), { GATE: "password" });
  assert.equal(res.status, 503);
});

test("off, everything goes on, unmarked", async () => {
  assert.equal(await gate(req(), { GATE: "off" }), null);
  const res = gated(new Response("x"), { GATE: "off" });
  assert.equal(res.headers.get("X-Robots-Tag"), null);
});

test("noindex: everything goes on without a password, marked not to index", async () => {
  assert.equal(await gate(req(), { GATE: "noindex" }), null);
  const res = gated(new Response("x"), { GATE: "noindex" });
  assert.equal(res.headers.get("X-Robots-Tag"), "noindex, nofollow");
  assert.equal(res.headers.get("Cache-Control"), null);
});

test("noindex: plain HTTP still goes to HTTPS", async () => {
  const res = await gate(new Request("http://roostack.cloud/x"), { GATE: "noindex" });
  assert.equal(res.status, 301);
});

test("served while on: not indexed, not cached by others", () => {
  const res = gated(new Response("x", { headers: { "Content-Security-Policy": "default-src 'self'" } }), env);
  assert.equal(res.headers.get("X-Robots-Tag"), "noindex, nofollow");
  assert.equal(res.headers.get("Cache-Control"), "private, no-store");
  assert.equal(res.headers.get("Content-Security-Policy"), "default-src 'self'");
});

test("plain HTTP goes to HTTPS before any password is asked", async () => {
  const res = await gate(new Request("http://help.roostack.cloud/backups/?x=1"), env);
  assert.equal(res.status, 301);
  assert.equal(res.headers.get("Location"), "https://help.roostack.cloud/backups/?x=1");
});

test("wrangler dev on this machine stays on plain HTTP", async () => {
  assert.equal((await gate(new Request("http://127.0.0.1:8787/"), env)).status, 401);
  assert.equal((await gate(new Request("http://localhost:8787/"), env)).status, 401);
});
