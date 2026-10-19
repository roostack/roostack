// A shared password in front of the whole help until launch (the Owner,
// 2026-10-19): HTTP Basic Auth, checked here before anything is served.
// Any user name; the password is the Worker's secret SITE_PASSWORD (from
// sops, set by Roostack Cloud's ops/site-deploy.sh). wrangler.jsonc's GATE
// var turns it on ("password") or off ("off", at launch). On, without a
// password set, every request is refused: the help is never served open
// by mistake. While it's on, every answer also says not to index it.
//
// Roostack Cloud's website (site/worker/gate.js in its repository) has
// the same gate: keep the two alike.

const realm = 'Basic realm="Roostack (not launched yet)", charset="UTF-8"';

/** gate answers a request the gate stops, or null when it may go on. */
export async function gate(request, env) {
  if (env.GATE === "off") return null;
  if (!env.SITE_PASSWORD) {
    return plain(503, "This site isn't open yet.\n");
  }
  if (await allowed(request.headers.get("Authorization"), env.SITE_PASSWORD)) return null;
  const res = plain(401, "This site isn't open yet: it needs its password.\n");
  res.headers.set("WWW-Authenticate", realm);
  return res;
}

/** gated marks a response served while the gate is on. */
export function gated(response, env) {
  if (env.GATE === "off") return response;
  const res = new Response(response.body, response);
  res.headers.set("X-Robots-Tag", "noindex, nofollow");
  res.headers.set("Cache-Control", "private, no-store");
  return res;
}

async function allowed(header, password) {
  const m = /^Basic\s+([A-Za-z0-9+/=]+)$/i.exec(header ?? "");
  if (!m) return false;
  let given;
  try {
    const raw = atob(m[1]);
    given = new TextDecoder().decode(Uint8Array.from(raw, (c) => c.charCodeAt(0)));
  } catch {
    return false;
  }
  const colon = given.indexOf(":");
  if (colon < 0) return false;
  // Compared as SHA-256 digests, so in constant time and at equal length.
  const [a, b] = await Promise.all([digest(given.slice(colon + 1)), digest(password)]);
  return crypto.subtle.timingSafeEqual ? crypto.subtle.timingSafeEqual(a, b) : same(a, b);
}

async function digest(s) {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}

// For Node's tests, which lack Workers' timingSafeEqual.
function same(a, b) {
  let d = a.length ^ b.length;
  for (let i = 0; i < a.length; i++) d |= a[i] ^ b[i];
  return d === 0;
}

function plain(status, text) {
  return new Response(text, {
    status,
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "no-store",
      "X-Robots-Tag": "noindex, nofollow",
    },
  });
}
