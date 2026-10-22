# Taking services off the internet quickly: the "big red button" (proposal, 2026-10-22)

**A proposal for the Owner. Nothing here is decided or built.** The short
version is SPEC.md §6.16; this document has the reasoning, the formats and
the questions. Mock-ups are in [docs/mockups/red-button/](mockups/red-button/README.md).

## The problem

A zero-day lands in something many web apps are built on: PHP, Log4j, a
Node library, an image decoder. Exploits are being tried within hours.
The people Roostack is for aren't sysadmins: they won't read the
advisory, work out which of their services use the affected thing, and
close them off by hand. And the fixed version of each app often takes
days to arrive.

What they need is a simple, safe way to **take public services off the
internet quickly, and put them back just as easily**. Better still,
Roostack would do it for them when it learns of such a problem, and undo
it once the fixed version is installed.

What exists today doesn't cover this:

- **Withdrawals** (§8.3.4) are per package version. They can stop a
  service, which also stops it for the household. They don't know about
  a problem across packages ("everything built on PHP").
- **CrowdSec's AppSec** (§6.10 item 4) blocks known exploits, but its
  rules are pinned to a Hub commit that arrives with Roostack releases.
  A rule for a new zero-day reaches labs days later, if it's written at
  all.
- **Making each address private** by hand (§6.11) works, but it means
  one service at a time, and the Owner has to know which services are
  affected. It also loses the choices made for that address (its parts
  kept private, its public records), so putting it back is work.

## Words

- **Block (public access):** visitors from the internet get a page saying
  the service is closed for now. People at home or on the lab's VPN use
  it as before. The service keeps running, and its settings, addresses
  and DNS records stay as they are. *Proposed UI word: "Pause public
  access". The question is at the end.*
- **Lift:** end a block.
- **Built with:** what a service's app is built on (PHP, Node, nginx,
  Log4j), as its package declares.
- **Advisory:** a security problem the service registry publishes,
  naming what's affected, how bad it is, and the fixed versions.
- **Way in:** how visitors from the internet reach the lab (§6.11):
  port forwarding, the lab's own edge, Roostack Cloud's relay, or a lab
  on the internet.

## 1. What there is to build on

- **Every way in ends at the controller's Caddy** (§10.5). Port
  forwarding reaches it on 443. The lab's own edge and Cloud's relay
  pass TLS through, undecrypted, to its edge listener (8474). A lab on
  the internet is reached directly. So **one change in the controller's
  Caddy covers every way in**, and it's the only place that can show a
  page (the edges never hold certificates).
- **Each public address is already one `route` block** (`caddy.Site`,
  `publicRoute`), with a matcher for outside visitors (`notPrivate`) that
  the private paths and AppSec use. A block is one more line in it.
- **Certificates use DNS-01** (§6.5), so nothing about renewals passes
  through ports 80 or 443. Blocking can't stop a certificate renewing.
- **Withdrawals** (§8.3.4) are the model for advisories:
  - They're signed in the registry's index.
  - A stop isn't stored. It's worked out from the index each time, so it
    lifts by itself.
  - The Owner can opt out, or start a service anyway.
  - A stolen signing key can at worst stop services until the Owner
    starts them again.
- **Error pages** (`internal/errpage`) already give each status Caddy
  answers with a page in the lab's look, naming the service.
- **Roostack Cloud's edges** already refuse visitors by country and by
  the lab's CrowdSec bans. They pick up a change within about 20 seconds,
  even while the lab is down.
- **What the registry runs on today** (22 packages, 2026-10-22):
  - mostly Node (Ghost, HedgeDoc, Immich's server, Linkwarden, Wiki.js
    and others);
  - Go (Forgejo, Gitea, Miniflux, Navidrome, ntfy, PhotoPrism);
  - Python (Mealie, Paperless-ngx, Radicale, Immich's machine learning);
  - one each of Rust (Vaultwarden), Java (Stirling-PDF) and .NET
    (Jellyfin);
  - Plex, closed source;
  - one PHP app: Lychee, on FrankenPHP, a PHP server built on Caddy.

  No package says any of this. The only structured facts are the
  database types and `about.source`.

## 2. What "block" means

### 2.1 One mechanism: the controller's Caddy shows outside visitors a page

A blocked public address keeps its route. Outside visitors are answered
with a page before anything else in the route runs; home visitors go
through as before:

```caddyfile
@lychee-web host photos.smiths.example.com
handle @lychee-web {
	vars {
		roostack_name lychee
		roostack_title "Lychee"
		roostack_private no
		roostack_blocked yes
	}
	@lychee-web-outside not {
		remote_ip 192.168.0.0/16 …
		not vars roostack_edge yes
	}
	route {
		error @lychee-web-outside 503
		…as before: private paths, CrowdSec, rate limits, AppSec, the service
	}
}
```

The `error` goes first *inside* the route block: Caddy sorts directives
side by side, and runs `route` before `error`, as it did with `abort`
(docs/ASSUMPTIONS.md P2). To check on a VM when it's built.

- **Applied like any change:** the block is in the desired state, the
  controller regenerates its Caddy files and Caddy reloads, in seconds.
  Only the controller's Caddy changes, so a blocked lab doesn't wait for
  other servers.
- **Public ports too:** a package's public ports (§8.1 `ports:`) don't
  go through Caddy. Blocking a service also makes its public ports home
  only at the firewall of the server it runs on. That server's firewall
  is updated with its next assignment. If the server can't be reached,
  the page says its ports are still open.
- **Considered:**
  - *Deleting the public DNS records.* Too slow both ways: resolvers
    cache them for their TTL, and phone apps would get "server not
    found" instead of a page. A roost.ac name's records go through
    Cloud's API, which can't delete. Undoing it would wait for DNS again.
  - *Closing the firewall.* Visitors would get no page, only a timeout.
    On a lab on the internet it would cut off Roostack's own pages too.
    It's kept as the deeper level below, for when Caddy itself can't be
    trusted.
  - *Stopping the service.* The household would lose it too, and that's
    a withdrawal's job.
  - *Making the address private.* That loses its public choices, and
    "private" is a setting rather than a temporary state.

### 2.2 Each way in

| Way in | Blocked (first version) | Closed (later: when Caddy itself is affected) |
|---|---|---|
| Port forwarding | Caddy's page | The firewall drops 80/443 from outside: visitors get a timeout |
| The lab's own edge | Caddy's page (the edge passes it through) | The edge's config leaves the names out (`caddy.Edge`'s `Hosts`): a closed connection |
| Roostack Cloud's relay | Caddy's page (the edges pass it through) | Cloud's edges close connections for the names, even if the lab is down (§9) |
| A lab on the internet | Caddy's page | The firewall: Roostack's pages are then reachable only through an SSH tunnel, and the page says so first |

### 2.3 Public DNS records

**They don't change.** A blocked address still resolves, so visitors get
the page, and lifting is instant. Monitoring and the public status page
show a blocked service as **Paused** (§6.15), not down.

### 2.4 What visitors see

A page from `internal/errpage`, in the same style as the others: a
picture, a light headline, plain words. Status 503, with
`Retry-After: 3600`, and no automatic reload (unlike "Still hatching").

> **Resting its wings.**
> {service} is closed to visitors from the internet for a while. Its
> owner will open it again soon. If you're at home, it's working as usual.

It doesn't say *why*. "Closed because of a security problem" tells an
attacker what to try once it's open again, and a block the Owner placed
for another reason (a holiday, a suspicion) would say something false.
*Question 4.*

Phone apps (Immich, Bitwarden, Jellyfin) see a 503 and show their own
"can't reach the server". That's the same as today when a service is
down.

### 2.5 What stays reachable

- **Home and the lab's VPN:** everything, as before (the private check).
- **Roostack's own pages:**
  - Never blocked by the button. On a lab on the internet they're
    public, protected by two-factor and AppSec, and the Owner needs them
    to lift the block.
  - An advisory about Roostack itself or Caddy is the "closed" level
    (§2.2, later), which asks first. *Question 5.*
- **Single sign-on's sign-in page** (`login.<domain>`, §5.2):
  - Roostack's own code, so an advisory about an app doesn't affect it.
  - It stays public while any public service that uses it is unblocked.
    When every one of them is blocked, it's blocked too, since it has
    nothing to sign people in to.
- **The public status page:** stays up. It's static files, and it's
  where visitors look.
- **Certificate renewals:** unaffected (DNS-01).
  - If TLS-ALPN-01 is ever built (§6.6, benched), its challenges are
    answered by Caddy's TLS handshake before any route runs, so they
    pass too.
- **The outside check link** (`/.well-known/roostack-check/…`): blocked
  with the rest. It tests whether visitors get in, and they don't.

## 3. What the button covers

| Scope | For | Proposed |
|---|---|---|
| **Everything public** | "Something's wrong, close it all now" | Yes: the button's default |
| **One service** | The Owner suspects one app; an advisory about one package | Yes: on the service's page |
| **What they're built with** (PHP, Log4j…) | A zero-day in one technology | Yes: a choice on the button's page, listing what's in the lab and which services each covers |
| **One way in** (only the relay, say) | — | No: an exploit doesn't care how the visitor came |
| **One address of a service** | — | No: a block is per service, all its public addresses and ports (like turning AppSec off, §6.10) |

**Where a block is kept** (desired state, so it's backed up and a
restore brings it back; that's the safe direction):

```yaml
# lab.yaml
public:
  block:
    all: true                # everything public
    built_with: [php]        # services built with these, now and later
    since: 2026-10-22T09:14:00+11:00
    by: tom
```

```yaml
# services/lychee.yaml (the instance)
public_block:
  since: 2026-10-22T09:14:00+11:00
  by: tom
```

- **A block by what services are built with also covers services
  installed later** that are built with the same thing: "everything on
  PHP" stays true. *Question 7.*
- **Blocks the Owner places don't expire.** A block that lifted by
  itself on a timer could reopen a vulnerable service while nobody's
  looking. Instead, Home keeps it on the attention list, and the weekly
  summary says how long it has lasted. *Question 8.*

## 4. Knowing what a service is built with

| Source | Accuracy | Cost and upkeep | On a small server |
|---|---|---|---|
| **Declared in the package** (`built_with:`), reviewed | Good for what matters: the runtime, web server and notable libraries, which a reviewer can see from the project | A line per package, reviewed once; changes rarely (an app doesn't move from PHP to Node between versions) | Nothing to run |
| **Detected from images** with an SBOM (Syft) and a scanner (Grype, Trivy) | Exact versions of everything in the image. But it's noisy: base-image packages the app never loads, and hundreds of "vulnerabilities" per image | A new tool. Its database (hundreds of MB) is refreshed daily | Too heavy for a Pi; also a new image and dependency |
| **The images' own labels** (OCI `org.opencontainers.image.*`) | Rarely says the runtime; varies by project | Free | Free |
| **Roostack's own knowledge** | Exact: Roostack pins the database images and builds Caddy | None beyond what exists | Nothing to run |

**Proposed:**

1. **Declared names in packages, reviewed.** A new top-level field:

   ```yaml
   built_with: [php, frankenphp, laravel]
   ```

   - **The name:** `built_with` rather than `components`, since Roostack
     already calls a service's containers its components (the
     `io.roostack.component` label).
   - **Per package, not per container.** A block is per service anyway;
     per container can come later if a page needs it.
   - **Names are short ids from a list the registry keeps**, with a
     title and a line about each (`php`: "PHP"; `log4j`: "Log4j, a Java
     logging library"), published in the index.
     - Adding one is a registry change, not a Roostack release.
     - Roostack checks only their form (lowercase letters, digits and
       `-`, up to 32), so a newer registry's names work on an older lab.
     - The registry's CI checks each name is on its list.
     - A package of your own may use any name; one the registry doesn't
       know shows as written.
   - **What goes on the list:** things that have had, or could have,
     internet-scale zero-days:
     - language runtimes: `php`, `node`, `python`, `java`, `dotnet`,
       `ruby`, `go`, `rust`;
     - web servers and app servers: `nginx`, `apache`, `caddy`,
       `frankenphp`, `tomcat`;
     - frameworks and libraries with a history: `log4j`, `spring`,
       `laravel`, `django`, `rails`, `nextjs`, `imagemagick`, `ffmpeg`,
       `libwebp`, `openssl`.
   - **Not a full bill of materials.** Its purpose is the button and
     matching advisories by name, not listing every dependency.
   - **Required for registry packages** (the policy, like `about:`):
     at least one name. The registry's maintainers fill in the 22
     packages.
2. **Roostack adds what it knows itself:**
   - a database of type `postgres`, `mysql`, `mariadb` or `redis` adds
     that name, with the exact version Roostack pins;
   - a custom database image still counts as its type.
   - Roostack's own parts (`caddy`, `crowdsec`, `restic`, `roostack`)
     are listed for the lab as a whole on Settings → Public access, for
     advisories about the front door (§5).
3. **Later, in the registry's CI, not on labs:**
   - Syft reads each new image once and records exact versions of the
     declared names in the index ("php 8.4.13"), which the service page
     can show;
   - a person still reviews the names.
   - A new tool for the registry's CI, so it's only a proposal (*question
     10*). Labs never scan.

## 5. Where advisories come from

| Option | Trust | False positives | Labs not linked to Cloud |
|---|---|---|---|
| **A. Each lab reads public feeds** (OSV, GitHub's advisories, CISA's Known Exploited Vulnerabilities) | Each feed is HTTPS from its publisher, but unsigned, and a lab can't judge them | High. Feeds name upstream projects and version ranges, not Roostack packages. A lab would have to map CPEs and ecosystems to its services itself, from incomplete data | Works |
| **B. The registry publishes signed advisories in its index** | Signed like withdrawals (`docs/REGISTRY-KEYS.md`). A person reviews each before it's published | Low. Maintainers name the exact package versions affected and fixed | Works: the registry isn't Cloud |
| **C. Roostack Cloud sends them to linked labs** | Another key and channel to trust. Cloud would need to know what each lab runs, which it doesn't (by design, §6.6) | Same as B, if Cloud took them from the registry | Not covered |

**Proposed: B**, with the feeds informing the maintainers rather than the
labs. Cloud only shortens the wait (§9).

- **In the registry repository**, an advisory is a reviewed file, like
  `registry.yaml`'s withdrawals:

  ```yaml
  # advisories/2026-0007.yaml
  title: PHP lets anyone run code through a crafted request
  cves: [CVE-2026-41234]
  built_with: php            # what it's about
  severity: critical         # critical, high or low (below)
  exploited: true            # being used in attacks (KEV, or the project says so)
  published: 2026-10-22
  link: https://github.com/roostack/services/security/advisories/…
  affects:
    lychee:
      versions: [7.11.1-roostack.1, 7.11.0-roostack.2]
      fixed: 7.11.2-roostack.1   # or left out: no fix yet
  local: possibly            # own packages built_with php: "possibly affected" (or "no")
  ```

- **In the index**, a new top-level `advisories` list (an additive field
  in format 1, which older Roostacks ignore). Each `affects` entry is
  turned into versions with their SHA-256, as withdrawals are, so a
  package of your own that shares a name and version isn't matched.
  CI refuses an advisory naming a version that doesn't exist.
- **Severity is Roostack's, not CVSS:**
  - **critical:** anyone on the internet can use it without signing in,
    to run code or read or change data.
  - **high:** serious, but needs an account, a setting that isn't the
    default, or more than a visitor can send.
  - **low:** worth knowing.

  Maintainers judge this for Roostack's setup. For example, a problem
  only on an admin page the package keeps home-only isn't critical here.
- **Matching:** the controller compares each installed service with the
  index after each fetch, the same way it does withdrawals
  (`markStops`). A service is affected when its version (and the lab's
  copy's SHA-256) is in `affects`.
  - A service from one of the Owner's own packages, whose `built_with`
    names the advisory's subject, is "possibly affected": a notice only,
    never an automatic block.
- **Trust:**
  - **What a forged advisory can do:** with the signing key stolen, at
    worst block public access, the same limit as a withdrawal's stop.
    It can't stop, delete or change anything, and it can't keep a
    service blocked against the Owner, who can choose **Keep it public
    anyway** (§7).
  - **Replay and freezes:** the index's serial already refuses an older
    index. A freeze (holding back new indexes) could hide a new
    advisory, but can't keep an old block in place: blocks are worked
    out from the current index. The 14-day stale warning (§8.3.3)
    covers freezes.
  - **Turning the registry off** (`registry.disabled`) means no
    advisories. Settings → Public access says so, and the button still
    works.
- **How the maintainers hear of problems** (later, the registry's
  tooling):
  - a scheduled job reads KEV daily, and OSV and GitHub's advisories for
    each package's `about.source` and `built_with`;
  - it opens an issue for each match, with what it found.
  - A person writes the advisory. Nothing auto-publishes an advisory
    that blocks.
- **How soon a lab hears:** today the controller fetches every 4 hours,
  plus up to 30 minutes. An unchanged index costs one small request
  (ETag), so **checking hourly** is cheap. Proposed: hourly for everyone
  (*question 9*). Cloud's nudge (§9) makes it minutes for linked labs.

## 6. Automation: the Owner's choice

Settings → Public access, **When the registry warns about a security
problem**:

- **Tell me.** An advisory shows on Home and is emailed. Nothing is
  blocked unless I block it.
- **Block it when it's being used in attacks** *(proposed default)*.
  Services affected by a critical advisory marked `exploited` are
  blocked by themselves; anything else, as "Tell me".
- **Block it for any critical problem.** Services affected by any
  critical advisory are blocked by themselves, exploited or not.

Whatever the choice:

- High and low advisories are never blocked automatically.
- "Possibly affected" (the Owner's own packages) is never blocked
  automatically.
- A service that isn't public isn't blocked: nothing to do, but the
  notice still says to update.

**The default.** "Block it when it's being used in attacks" is proposed
for new labs. The people Roostack is for won't act on an email within
hours, and an exploited, critical, unauthenticated problem in a public
app is exactly when hours matter. Being blocked costs them only access
from outside, which comes back by itself once the update is in.
*Question 2.*

**Where it shows:**

- **Home:**
  - A block in force is an attention item at the top: "Public access is
    paused for 2 services (Lychee, Ghost) because of a security problem
    in PHP." It links to a page listing what's blocked and why, with
    **Update** where a fix exists.
  - A block placed by a person reads "Public access is paused for
    everything since 09:14, by tom."
  - An advisory that didn't block (Tell me, or high) is a warning.
- **The service's page:**
  - a notice with the advisory (title, its link, said by the service
    registry, since when), what Roostack did, and **What to do** (the
    update, or "no fix yet");
  - "Reachable from" reads "Home only for now (public access paused)".
- **Email:**
  - an automatic block or an advisory is emailed straight away to the
    **Security** group (it doesn't wait 10 minutes like Problems);
  - lifting is emailed too ("Lychee is public again: 7.11.2 fixed the
    problem").
  - The weekly summary lists blocks in force and how long they've lasted.
- **Roostack Cloud:**
  - the lab's report (§6.6) gains a line: whether public access is
    paused, since when, and how many services;
  - Cloud's portal shows it, and Cloud can email the lab's confirmed
    recipients when the lab itself has no email set up.
  - Never which services or which advisory: Cloud doesn't learn what the
    lab runs.

## 7. Lifting

- **A block from an advisory lifts by itself**, because it's worked out
  each time from the index:
  - when the service's version is no longer in `affects` (it was
    updated);
  - when the registry withdraws or changes the advisory;
  - or when the Owner chooses **Keep it public anyway**. That's stored
    in the instance as `public_anyway: 2026-0007`, like `started_anyway`.
    It ends when the version changes, and it's audited. Its confirmation
    names the risk: "Anyone on the internet can try this. It's being
    used in attacks now."
- **A block placed by a person** is lifted by a person, the Owner (§8):
  - a confirmation lists what will be public again;
  - if an advisory still affects any of those services, it says so and
    leaves those blocked ("Lychee stays paused: it's still affected by
    the PHP problem. Keep it public anyway…").
- **Updates:**
  - the notice offers the fixed version straight away (§7.1's **Update**,
    with its backup first);
  - when the service's update policy allows that version (not `notify`),
    Roostack updates it **at once** rather than at night, as it already
    does for a service stopped by the registry (`UpdateStopped`). The
    service isn't down at home, but it's down for everyone else.
    *Question 6.*
  - The block lifts only once the update's check has passed. If the
    update fails and Roostack goes back to the old version, the old
    version is still affected, so it stays blocked.
- **No fix yet:** the service stays blocked, and its notice says "No
  fixed version yet. Roostack is checking the service registry every hour."
  It offers **Keep it public anyway…** and **Remove <app>…**. There's no
  automatic way out but the fix.

## 8. Safety and abuse

- **Who:**
  - **blocking:** the Owner and Admins. It only ever reduces what's
    reachable, and an Admin who sees a problem at 2 am shouldn't have to
    wake the Owner.
  - **lifting**, and **Keep it public anyway:** the Owner only.
    Exposing something to the internet is the Owner's call everywhere
    else (§6.11).
  - Members see that it's paused, nothing to press.
  - *Question 1.*
- **Quick, but not by accident:**
  - Home has **Pause public access…** while anything is public. It's a
    link, red-outlined, not a primary button.
  - It opens a page rather than a dialog, so it works well on a phone.
    The page has what to pause (everything, preselected; or what
    they're built with; or chosen services), what will happen ("Visitors
    from the internet will see a page saying it's closed for now. At
    home it will keep working."), and one red **Pause now**.
  - Two taps on a phone, with no typing.
  - Lifting is a confirmation dialog with the warning above.
- **Audit log:**
  - a person blocking or lifting (scope, and who);
  - Roostack blocking or lifting by itself (which advisory, which
    services);
  - Keep it public anyway;
  - a change to the automation setting.
  - Each block or lift by a person is also emailed to the Owners straight
    away (Security group), so an Admin's block, or one nobody expected,
    is seen.
- **When parts of the lab are broken:**
  - The block only needs the controller's Caddy. It doesn't wait for
    other servers, backups or the registry.
  - **From the command line, without the web UI:** `roostack public
    pause [--service NAME | --built-with NAME]`, `roostack public resume`
    and `roostack public status`, run with sudo on the controller.
    - With the daemon running, they ask it (as `roostack user` commands
      do).
    - Without it, they write `lab.yaml` and the Caddy files themselves
      and reload Caddy, so they work even when Roostack won't start.
    - The help says how, and gives the last resort: stopping Caddy
      (`systemctl --user stop roostack-caddy` as the roostack user),
      which takes everything off, home included.
  - If Caddy can't be reloaded (an error in its config), the block says
    so at once, as a problem. Later, "closed" (§2.2) can fall back to
    the firewall.
- **Reaching the button from a phone, away from home:** Roostack's
  pages are private on a lab at home, so this needs one of:
  - the lab's VPN (Tailscale, or Roostack's own later);
  - Roostack Cloud (§9), for linked labs;
  - SSH, for technical users.

  The help says so, and Settings → Public access suggests setting one up.

## 9. Roostack Cloud's part (later, in Cloud's repository)

What exists (Cloud's DESIGN §5):

- Edges pull their config every 15 seconds and refuse visitors by
  country and by the lab's bans with a bare closed connection. They
  can't show a page: they never decrypt.
- **Turn the relay off** on the account's lab page works while the lab
  is down. But it gives the relayed names back the lab's own A records,
  so it isn't a block where the lab is also reachable directly.
- Cloud only answers the lab's requests. It has no way to push to a lab.

Proposed, in this order:

1. **A nudge.** The relay's 5-minute `GET /v1/lab/relay`, and a new
   small `GET /v1/lab/news` for linked labs that aren't relayed, say
   "the registry has something new". The lab then fetches the registry
   at once.
   - Cloud learns of a new index from the registry's CI, or by polling
     it.
   - Cloud gains no authority here: the advisory still comes signed from
     the registry, and the lab checks it as usual.
   - Cost: one tiny request every few minutes per linked lab. *Question
     11.*
2. **Pause at the edges, from Cloud's website** (relayed labs). A new
   relay state, `paused_at`, beside `stopped_at`:
   - the edges close connections for the lab's names;
   - DNS keeps pointing at the edges, unlike turning the relay off, so
     the lab isn't reachable another way;
   - it works with the lab down or compromised, within about 20 seconds.
   - Lifted from Cloud's page, or by the lab's Owner from Public access.
   - Visitors get a connection error, not a page: Cloud's page should
     say so.
   - Cloud should ask for a fresh two-factor code (its `confirmForm`),
     and record it.
   - The lab sees it in the relay's answer and shows it as a block.
3. **Pause a lab that isn't relayed, from Cloud's website** (opt-in, on
   the lab: "Let me pause public access from Roostack Cloud"):
   - Cloud queues it, and the lab picks it up at its next nudge check
     (item 1) and pauses itself;
   - only pausing: lifting is done on the lab.
   - A compromised Cloud could then pause opted-in labs' public access,
     the same limit as a stolen registry key, which is why it's opt-in.
   - *Question 11.*
4. **Cloud's own emails**, from the lab's report (§6), when the lab
   can't send email itself.

Cloud never learns which services a lab runs or which advisories affect
it. The report says only "paused since …, N services".

## 10. The registry's part

- **`built_with:`** for every package, reviewed, and the list of names
  with their titles (`built-with.yaml` at the top of the repository,
  published in the index).
- **`advisories/`**, one file per advisory, reviewed by the registry's
  maintainers (code owners), and built into the signed index by
  `roostack registry index`.
- **Publishing quickly:**
  - an advisory merged to `main` publishes within minutes, as today;
  - the update bot (§8.3.8, not built) then brings the fixed version.
    For a zero-day, a maintainer may publish the fix by hand first.
- **Later:**
  - the watcher (KEV, OSV, GitHub's advisories → issues);
  - Syft in CI for exact versions (§4).

## 11. The first version, and what comes later

**First: the button** (no advisories yet; useful on its own):

- `public.block` in `lab.yaml` and `public_block` in the instance:
  - Caddy's page for outside visitors, via `caddy.Route.Blocked`;
  - the new error page in `internal/errpage`;
  - public ports closed at the firewall;
  - the sign-in page following the services that use it;
  - monitoring and the status page showing **Paused**.
- `built_with:` in the package format:
  - `pkgspec`, its policy, the package editor (`check.js`, `schema.js`,
    the corpus) and the help's format reference;
  - Roostack's own names for databases;
  - the registry's maintainers fill in every package.
- The UI:
  - Home's **Pause public access…** and its page (everything, by what
    they're built with, or chosen services);
  - the attention items;
  - the service page's notice and its own **Pause public access…**;
  - Settings → Public access listing blocks;
  - lifting (the Owner).
- Roles, the audit log, Security emails, the weekly summary.
- `roostack public pause | resume | status`.
- Tested:
  - the generated Caddy files (golden), on a VM;
  - each way in: port forwarding, the lab's own edge and the relay,
    from outside;
  - a phone width.

**Second: advisories from the registry:**

- the `advisories` field in the index and the registry's files, with
  CI's checks;
- matching on the controller;
- the automation setting;
- blocks worked out each time, and lifting by themselves;
- **Keep it public anyway**;
- updating at once;
- hourly fetching;
- emails;
- the report line for Cloud.

**Third: Cloud and the deeper level:**

- the nudge;
- pausing at the edges from Cloud's website;
- the opt-in pause for labs that aren't relayed;
- "closed" (firewall, edge, relay) for advisories about the front door
  (Caddy, Roostack).

**Fourth: the registry's tooling:**

- the watcher;
- Syft in CI (if agreed);
- exact versions on the service page.

## 12. Questions for the Owner

1. **Who:** Admins may pause, only the Owner lifts or keeps something
   public anyway? *(Recommended.)* Or the Owner only, for both?
2. **The default for new labs:**
   - "Block it when it's being used in attacks" *(recommended)*;
   - "Tell me";
   - "Block it for any critical problem".
3. **The word:**
   - "Pause public access" *(recommended: it says temporary, and "block"
     sounds like a firewall rule)*;
   - "Block public access";
   - "Take off the internet".
4. **What visitors see:**
   - a page that doesn't say why *(recommended)*;
   - or one that says it's closed while a security problem is fixed.
5. **Roostack's own pages on a lab on the internet:** never paused by the
   button *(recommended)*, with "closed" (SSH tunnel only) a separate,
   later choice?
6. **Updating at once** when a fix arrives for a blocked service whose
   update policy allows it (as for registry stops)? *(Recommended: yes,
   not on `notify`.)*
7. **A block by what services are built with covers services installed
   later?** *(Recommended: yes.)*
8. **Blocks placed by a person never expire** *(recommended)*, or end
   after a time the Owner picks?
9. **Fetching the registry hourly** instead of every 4 hours, and
   advisories inside the index rather than a file of their own?
   *(Recommended: both. One signature, one serial to protect against
   replays, and an unchanged index costs one small request.)*
10. **Syft in the registry's CI** for exact versions: worth a new tool
    there later? (Never on labs.)
11. **Cloud:**
    - the nudge for linked labs *(recommended)*;
    - pausing at the relay's edges from Cloud's website *(recommended)*;
    - letting Cloud ask labs that aren't relayed to pause themselves
      *(opt-in; recommend later, if at all)*.
12. **`built_with`:** the field's name, per package rather than per
    container, and its list of names kept by the registry rather than
    in Roostack's code?
13. **The phases** as in §11: the button first, advisories second?
