# Running the OpenID conformance suite

The sign-in page for services is an OpenID Connect provider written for Roostack (SPEC.md §5.2). The OpenID Foundation's conformance suite is its check: run it again before a release that changes `internal/sso` or `internal/web/signin.go`. Last run: 2026-10-20, suite release 5.3.3, both plans passing (docs/ASSUMPTIONS.md V7).

What it takes, about an hour the first time:

- a dev VM with a lab on a test address, two OIDC services with single sign-on on, and a Member to sign in as;
- a test-only build of Roostack that also accepts the suite's redirect address;
- the suite itself, in Docker (it isn't vendored here: clone it each time from the tag you mean to test with).

## 1. The lab

On a free VM (here VM 4: its port 443 is 48443 on the dev machine):

```sh
ROOSTACK_VM=4 scripts/dev-vm.sh up
make build && ROOSTACK_VM=4 scripts/dev-vm.sh install
```

Append to `/var/lib/roostack/lab.yaml` a test address whose names resolve to 127.0.0.1, and Caddy's own certificates:

```yaml
name: Smiths
domain: smiths.127-0-0-1.nip.io
email: sam@example.com
dns:
  provider: cloudflare
  directive: issuer internal
```

Then, on the VM: `sudo roostack user add sam -role owner`, `sudo roostack user add mum -role member`, `sudo ROOSTACK_SECRET_ROOSTACK_CADDY_DNS_API_TOKEN=dummy roostack apply`, and restart `roostack`. In the UI (`scripts/dev-vm.sh ui`): sign in as sam, finish the first-run steps, add `internal/testdata/packages/mealie` as one of your own packages, install it twice (the second copy named `two`), and turn single sign-on on for both. Sign in once as mum to choose a password, and give mum an email address on People.

The suite needs two clients (one test checks a refresh token can't be used by another app): `mealie` and `mealie-two`. Their secrets are the Podman secrets `roostack-mealie-sso-client-secret` and `roostack-mealie-two-sso-client-secret` (as the roostack user: `podman secret inspect --showsecret --format '{{.SecretData}}' …`).

## 2. The test-only build

The suite signs in from its own address, which no package lists. A build with the `ssoconformance` tag adds the addresses in `ROOSTACK_SSO_CONFORMANCE_REDIRECTS` to every OIDC app (`cmd/roostack/signin_conformance.go`); release builds never contain it.

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags ssoconformance -o roostack-conf ./cmd/roostack
```

Copy it to the VM's `/usr/bin/roostack` (after `dev-vm.sh install`, which builds without the tag) and add a drop-in, `/etc/systemd/system/roostack.service.d/conformance.conf`:

```ini
[Service]
Environment=ROOSTACK_SSO_CONFORMANCE_REDIRECTS=https://localhost.emobix.co.uk:8443/test/a/roostack/callback
```

`roostack` here is the suite configuration's `alias`.

## 3. The suite

```sh
git clone --depth 1 --branch release-v5.3.3 https://gitlab.com/openid/conformance-suite.git
cd conformance-suite
MAVEN_CACHE=~/.m2 docker compose -f builder-compose.yml run --rm builder
```

Its containers reach the VM through a small relay named as the sign-in host, in a compose override of your own:

```yaml
services:
  login-relay:
    image: alpine/socat:latest
    command: TCP-LISTEN:443,fork,reuseaddr TCP:host.docker.internal:48443
    networks:
      default:
        aliases: [login.smiths.127-0-0-1.nip.io]
```

```sh
docker compose -p oidfconf -f docker-compose-dev-mac.yml -f override.yml up -d
```

The suite trusts any certificate, so Caddy's own CA is fine. Its results pages are at `https://localhost:8443`.

## 4. The configuration

A JSON file (keep it out of the repository: it holds the client secrets and mum's password):

- `alias`: `roostack`; `server.discoveryUrl`: `https://login.smiths.127-0-0-1.nip.io/.well-known/openid-configuration`;
- `client` and `client2`: `client_id` and `client_secret` for `mealie` and `mealie-two`;
- `browser`: for `https://login.smiths.127-0-0-1.nip.io/authorize*`, an optional task on `…/sign-in*` that types mum's username and password and clicks `button[type=submit]`, then a task on `*/test/*/callback*` that waits for `submission_complete`;
- `override`: for `oidcc-prompt-login` and `oidcc-max-age-1`, the same browser with `["wait", "xpath", "//*", 10, "Sign in", "update-image-placeholder-optional"]` before signing in; for `oidcc-ensure-registered-redirect-uri` and `oidcc-ensure-request-object-with-redirect-uri`, one task on `…/authorize*` waiting for the text `an address Roostack doesn` with `update-image-placeholder`.

The suite's own example of this shape is `.gitlab-ci/local-provider-oidcc-conformance-config.json`.

## 5. Running it

The suite's runner, in its own image (`docker build -t oidfconf-runner test`), on the suite's network:

```sh
docker run --rm --network oidfconf_default -v "$PWD/scripts:/suite:ro" -v "$CONFIG_DIR:/run" \
  -e CONFORMANCE_SERVER=https://nginx:8443/ -e CONFORMANCE_DEV_MODE=1 oidfconf-runner \
  python3 /suite/run-test-plan.py --no-parallel --export-dir /run/results \
  'oidcc-basic-certification-test-plan[server_metadata=discovery][client_registration=static_client]' /run/config.json \
  oidcc-config-certification-test-plan /run/config.json
```

Expect 0 failures, five warnings (SPEC §5.2 says why each is deliberate), and four tests to review: open each and check the page it recorded, which should be the sign-in form a second time or the unknown-address page with no redirect. Afterwards, `docker compose -p oidfconf down`, remove the images, and destroy the VM.
