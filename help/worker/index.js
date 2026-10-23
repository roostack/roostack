// The public help's only code on Cloudflare (SPEC.md §6.3.1): until launch,
// the help's gate (gate.js, the same gate as roostack.cloud's) in front of
// its static files: a password on the preview, and on help.roostack.cloud
// only a header asking search engines not to index it. At launch, with the gate off, this can go
// and the help is plain static files again.
import { gate, gated } from "./gate.js";

export default {
  async fetch(request, env) {
    const stop = await gate(request, env);
    if (stop) return stop;
    return gated(await env.ASSETS.fetch(request), env);
  },
};
