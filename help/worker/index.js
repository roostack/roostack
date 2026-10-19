// The public help's only code on Cloudflare (SPEC.md §6.3.1): until launch,
// the help's password (gate.js, the same gate as roostack.cloud's) in
// front of its static files. At launch, with the gate off, this can go
// and the help is plain static files again.
import { gate, gated } from "./gate.js";

export default {
  async fetch(request, env) {
    const stop = await gate(request, env);
    if (stop) return stop;
    return gated(await env.ASSETS.fetch(request), env);
  },
};
