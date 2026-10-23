#!/usr/bin/env bash
# Builds the help (strict: a broken link fails) and publishes it on
# Cloudflare (SPEC.md §6.3.1), as the preview on workers.dev, marked not to
# be indexed. Needs uv (the build) and Node (Wrangler, run with npx at the
# version in .github/versions.env), and a Cloudflare login (npx wrangler
# login).
#
#   scripts/help-deploy.sh                # build and publish the preview
#   scripts/help-deploy.sh --production   # build and publish help.roostack.cloud
#   scripts/help-deploy.sh --build-only   # build and write headers, to try with
#                                         # npx wrangler dev in help/
set -euo pipefail
cd "$(dirname "$0")/../help"
mode=${1:-preview}
wrangler=wrangler@$(sed -n 's/^WRANGLER_VERSION=//p' ../.github/versions.env)
uv run zensical build --strict

# Security headers for every page, served by Cloudflare from site/_headers.
# Scripts: only the site's own files, and each inline script by its
# SHA-256 hash, worked out from the built pages so they always match.
# Styles allow 'unsafe-inline': Zensical's search box injects a stylesheet
# (the same reasoning as the help inside Roostack, SPEC.md §6.3.1). The
# other headers are the website's (site/tools/headers.mjs in Roostack
# Cloud's repository): keep the two alike.
python3 -I - site "$mode" <<'PY'
import base64, hashlib, pathlib, re, sys
site = pathlib.Path(sys.argv[1])
hashes = set()
for page in site.rglob("*.html"):
    for attrs, body in re.findall(r"<script([^>]*)>(.*?)</script>", page.read_text(), re.S):
        if "src=" not in attrs and body.strip():
            hashes.add("'sha256-%s'" % base64.b64encode(hashlib.sha256(body.encode()).digest()).decode())
csp = "; ".join([
    "default-src 'self'",
    "script-src 'self' " + " ".join(sorted(hashes)),
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data:",
    "font-src 'self'",
    "connect-src 'self'",
    "worker-src 'self' blob:",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
])
(site / "_headers").write_text("/*\n" + "".join(f"  {k}: {v}\n" for k, v in [
    ("Content-Security-Policy", csp),
    ("Strict-Transport-Security", "max-age=31536000"),
    ("X-Content-Type-Options", "nosniff"),
    ("Referrer-Policy", "strict-origin-when-cross-origin"),
    ("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()"),
    ("Cross-Origin-Opener-Policy", "same-origin"),
] + ([] if sys.argv[2] == "--production" else [("X-Robots-Tag", "noindex, nofollow")])))
print(f"_headers: {len(hashes)} inline scripts hashed")
PY

case $mode in
--build-only) ;;
--production) npx --yes "$wrangler" deploy --env production ;;
*) npx --yes "$wrangler" deploy ;;
esac
