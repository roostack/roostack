# Your data and your keys

Roostack keeps your services and their data on your own computer. This
page says, without asking you to take anything on trust, where the keys
that protect your data are, what Roostack sends off your server, to whom
and why, and how you can watch it and check it yourself.

## The short version

- **Your backups are encrypted on your server, before they leave it,**
  with keys made on your server. The key that opens them all, your
  [recovery key](start/recovery-key.md), is on your main server and with
  you: nowhere else. Not at Roostack Cloud, and not with the people who
  make Roostack.
- **On the server itself, files aren't encrypted.** They're protected by
  who may read them: only Roostack (and the computer's administrator).
  Someone who takes the server, or its disk, could read them, unless you
  set up disk encryption when you installed Debian.
- **Roostack sends only what each thing you use needs:** the list is
  [below](#what-roostack-sends). There's no tracking and no usage
  statistics.
- **You can check all of this:** the lab's own record of what it told
  Roostack Cloud, the server's log, its network traffic, your backups
  with restic itself, and Roostack by rebuilding it from its source.

## Where the keys live

Your main server (the *controller*, the one that runs Roostack's pages)
holds most of the keys. Other servers in your lab hold their own.

| Key | What it does | Where it is | Does it leave the server? |
|---|---|---|---|
| **Recovery key** | Opens every backup of every server in the lab | On the controller, and the copy you keep | **Never.** It isn't even in the lab's own backups. If the lab is linked to Roostack Cloud, a public key worked out from it goes there ([below](#restoring-without-the-old-server)), and the recovery key can't be worked out from that |
| **A server's own backup key** | Opens that server's backups, and only those (other servers only: the controller's backups use the recovery key) | On that server | Made on the controller, which sends it to that server over the lab's private network |
| **The lab's certificate authority** | Lets the lab's servers prove who they are to each other | Its key is on the controller; each server has its own key and certificate | No (only in the lab's own, encrypted, backups) |
| **The lab's network keys** (WireGuard) | Encrypt everything between your servers, and the links to a friend's lab or to Roostack Cloud's relay | On each server | Only their public halves. A join code carries a key for the first connection, which the new server replaces with its own straight away |
| **Roostack Cloud certificate** | Proves to Roostack Cloud that requests come from your lab | On the controller | No: only a request for the certificate, which holds the public half. It's renewed with a new key |
| **Roostack Cloud storage keys** | Let a server add its backups to its own folder in Roostack Cloud's storage | On each server that backs up there | They come *from* Roostack Cloud, last 12 hours, and are left out of backups (SPEC §6.9) |

Your services' own passwords (made when they were installed), the login
for your domain's DNS, the password for sending email, and the keys of any
online storage you use are on the server too, in files only Roostack can
read. People's passwords are kept only as a one-way hash.

??? note "For technical users: the files"

    All in `/var/lib/roostack` (`0750 roostack:roostack`, so other users
    can't even list it), each `0600` unless it says otherwise:

    | File | Holds |
    |---|---|
    | `backup-key` | On the controller, the recovery key; on any other server, that server's own key for its main backup. While the key is being changed, also `backup-key.new`, `backup-key.old`, and `backup-key.change.json`. None of these is ever backed up |
    | `backup-copies/<name>.key` | On a server other than the controller, its key for each extra backup |
    | `backup-key-hints.json` | Which of a backup's key files each key opens (hashes and key IDs, no keys), so restic doesn't try others first (SPEC §10.10) |
    | `labca/ca.key`, `ca.crt` | The lab's certificate authority, on the controller: ECDSA P-256, 20 years |
    | `labnet/node.key`, `node.crt`, `ca.crt` | This server's key and certificate for the lab API (port 8471): a year, renewed 120 days before it ends |
    | `wireguard-keys.json` | A copy of the WireGuard keys, so a restored lab keeps its network. The keys themselves are in `/etc/wireguard/roostack*.conf` (`0600 root`) |
    | `cloud/lab.key`, `lab.crt`, `link.json`, `requests.log` | The link to Roostack Cloud ([Roostack Cloud](settings/cloud.md#what-your-lab-sends-and-keeps)) |
    | `cloud-backup-keys.json`, `cloud-prune-keys.json` | Roostack Cloud's short-lived storage keys; never backed up |
    | `mail-password` | The password for sending email |
    | `lab.yaml` (`0640`) | Settings, including this server's online-storage keys for its backups |
    | `roostack.db` | People (passwords as argon2id hashes, sessions as SHA-256 hashes, two-factor secrets as they are), and the online-storage accounts' keys |

    Services' generated passwords and the DNS login are Podman secrets, in
    `/var/lib/roostack/home/.local/share/containers/storage/secrets`. Every
    file Roostack keeps is in [Where files live](reference/files.md).

    The recovery key is 32 random bytes (256 bits), written as 64 hex
    digits in groups of four; a new one from **Change recovery key** is
    made the same way. A server's own backup key is 24 random bytes. In restic, every key only unlocks
    the repository's own master key, so any key that opens a backup can
    read all of it.

## What's encrypted, and with what

| What | How |
|---|---|
| **Backups, wherever they go** (a USB drive, online storage, another server, a friend's lab, Roostack Cloud) | restic: AES-256 with Poly1305 to detect any change, and each key protected with scrypt. Encrypted on your server, before anything is written to the destination |
| **Between your servers** | WireGuard, and inside it the lab API with mutual TLS 1.3: a stolen network key alone isn't enough |
| **To Roostack Cloud** | HTTPS (TLS 1.2 or newer), with your lab's certificate |
| **Your services and Roostack's pages** | HTTPS with Let's Encrypt certificates, made and renewed by Caddy on your server |
| **Visitors through Roostack Cloud's relay** | Their HTTPS connection ends in Caddy on your server: the relay passes it on still encrypted, and never has your certificates |
| **Email Roostack sends** | TLS to your email provider (Roostack refuses a provider that doesn't offer it). After that it's ordinary email, which email providers can read |
| **On the server itself** | Nothing extra: files, the database, and saved passwords are protected by who may read them, not encrypted. Disk encryption, if you want it, is Debian's, set up when you install it |

??? note "For technical users"

    restic is run as a command (`RESTIC_PASSWORD_FILE`, never the key on a
    command line) with its defaults: repository format 2, compression
    `auto`. The encryption is restic's own design: AES-256-CTR with a
    Poly1305-AES MAC, keys derived with scrypt (its parameters are in each
    key file). Backups between servers go to a restic rest-server over
    plain HTTP inside WireGuard; the data is restic's, encrypted already.
    Relayed visitors reach Caddy's edge listener (port 8474) through the
    firewall's redirect, with a PROXY header; Caddy then does TLS itself
    (SPEC §10.5).

## What Roostack sends off the server { #what-roostack-sends }

Everything below is something you turned on, or something Roostack needs
to keep itself and your services up to date. Roostack sends no usage
statistics, crash reports, or tracking of any kind, and sets no identifying
browser name in its requests.

### Roostack Cloud

Only if you [link your lab](settings/cloud.md) to it, and only from the
controller: your other servers never talk to Roostack Cloud. It goes to
`https://api.roostack.cloud`, and a roost.ac name's records and
certificate checks to Roostack Cloud's DNS (its "challenge server").
Each request Roostack makes is in the lab's
[request log](#the-request-log), apart from reading back its DNS records;
what Caddy and restic send is below.

| When | What's sent | Why |
|---|---|---|
| **Linking** | Your lab's name, the roost.ac name you'd like, Roostack's version, a certificate request (a public key), and the address of the page to come back to (the address your browser used for your lab, such as `https://192.168.1.20:8443/…`). Then, every 3 seconds until you confirm (10 minutes at most), the link's secret | So Roostack Cloud can show you which lab you're confirming, and give it a certificate |
| **Choosing a name** | The name you're typing, to check it's free; then the name you chose, and whether to keep your lab's roost.ac name until now working beside it. Later, that the old name can go (once your old addresses stop), or that the new one can, if your lab couldn't use it | Your roost.ac address |
| **Once a day**, and when Roostack starts | A check-in, with nothing in it: Cloud answers with the account, your names, and anything held back. Every hour instead while something is waiting on Cloud | To show you how the link stands |
| **After each check-in** | The lab's name; the email addresses of the people here who get the lab's problems by email; and, for each server backing up to Roostack Cloud, its ID, its name, and when its last backup there finished | Cloud's emails about your lab go to those people too, once each agrees; your lab's page there shows the backups |
| **Every two months or so** | A new certificate request (a new key) | The certificate renews itself before it ends |
| **After linking, and when the recovery key changes** | A public key worked out from the recovery key, and a signature that proves the lab has the key | [Restoring without the old server](#restoring-without-the-old-server) |
| **Backups to Roostack Cloud** | Before backing up, and before the weekly clean-up: which server, and what for (backing up, or cleaning up). After each check-in, and when you choose where backups go: a question, with nothing in it, about how much space the lab uses | Cloud answers with storage keys for 12 hours, for that server's folder only. The space is shown on the backup, and on Home from 80% full |
| **Your roost.ac name's DNS records**, when they change | Your server's address on your home network (`*.smiths.roost.ac` and `smiths.roost.ac` point there), and, for services you make public, your home's internet address. Every few minutes Roostack also reads them back, which sends only their names; those reads aren't in the request log | Your services' addresses |
| **Restoring on a fresh install** | A public key worked out from the recovery key, then a signature that proves you have the key | Cloud answers with keys to read your backups there. The restored lab's request log keeps these requests |
| **The relay**, if public visitors come in through it | When it's set up or changed: your public services' names, the relay's WireGuard public key, which countries may visit, your time zone, and your own domain (if relayed). Every 5 minutes: a check on it, and the addresses your lab's attack protection (CrowdSec) has banned, so the relay turns them away too | To bring visitors to your lab, and keep attackers out before they reach it |
| **Unlinking** | That the lab is leaving | |

Also sent to Roostack Cloud, but not through the request log:

- **Your backups** (if you back up there): restic's encrypted files,
  straight to Roostack Cloud's storage.
- **Certificate checks:** when Caddy gets or renews a certificate, it
  writes Let's Encrypt's check value to Roostack Cloud's challenge server
  itself, so Roostack doesn't see it. When you first get a name, Roostack
  writes a test value the same way; that one is in the request log.
- **Visitors through the relay**, still encrypted.

### Everyone else

| To | What, and when | Why |
|---|---|---|
| **The service registry** (`registry.roostack.cloud`) | Downloads only: the signed list of services every 4 hours and when you open **Add a service**, then the files of what you install. Nothing about your lab is sent | To offer services and their updates. Every file is checked against Roostack's signature |
| **Roostack's updates** (`apt.roostack.cloud`) and **Debian's** (your server's Debian mirror) | Downloads, each night after the backup; the list of Roostack's releases every 6 hours | Updates, checked against Roostack's and Debian's signing keys |
| **Container registries** (such as `docker.io`, `ghcr.io`) | Downloads of each service's images, exactly the versions its package names; Caddy's, which is built on your server with its plugins from Go's module mirror | To run your services |
| **Let's Encrypt** (or ZeroSSL, if it can't) | The lab's email address (from setup) and the names on the certificate | HTTPS certificates |
| **Your domain's DNS provider** (Cloudflare, Route 53, Porkbun, Gandi, GoDaddy, Namecheap, or Spaceship), if you use your own domain | DNS records: your server's home-network address for `*.yourdomain`, your home's internet address for public services, and certificate checks | Your services' addresses |
| **DNS servers** | While something is public, every 5 minutes, two DNS questions to OpenDNS and Google that answer with your home's internet address (Namecheap's API also needs `icanhazip.com`); DNS checks through Cloudflare (`1.1.1.1`) and Quad9 (`9.9.9.9`) | To keep your public addresses right when your internet address changes |
| **CrowdSec**, while anything is public | Its rules, and, unless you turned sharing off, the addresses of attackers it saw and what they tried | Protection for public services ([Public access](services/public.md)) |
| **Where your backups go** | restic's encrypted files only | Your backups |
| **Your email provider** | The emails Roostack sends: problems, security events, the weekly summary | So you hear about them |

With Roostack Cloud turned off (`cloud: { disabled: true }`, see
[Roostack Cloud](settings/cloud.md#turning-roostack-cloud-off)), none of
the Roostack Cloud list will happen. The registry and Roostack's updates
are separate services and will carry on.

## What Roostack Cloud can and can't see { #what-roostack-cloud-can-and-cant-see }

What Roostack Cloud **can see** is what's listed above, and:

- your home's internet address, and when your lab gets in touch;
- for a roost.ac name, every record of it, which is public anyway, as all
  DNS is: so your server's home-network address too. Private services'
  names aren't in it: one record covers them all;
- with the relay: who visits your public services (their internet
  address, country, and how much they send and receive), but not what
  they see or send, which stays encrypted;
- with backups there: how big your backups are and when they're made, and
  restic's key files, which are encrypted but say in plain text when each
  key was added and by which server (its host name, or Roostack's ID for
  it).

What Roostack Cloud **can't see**:

- **Your backups' contents.** It never gets your recovery key or any key
  that opens a backup. Its storage keys only let your servers write, and
  your lab clean up; they don't decrypt anything.
- **Your services' traffic, through the relay or otherwise.** Your
  certificates and their keys stay on your server.
- **Anything else on your server.** It only gets what your lab sends it.

To be exact about "can't": Roostack Cloud runs the DNS for roost.ac names,
so it could in principle get a certificate of its own for your roost.ac
name. Anyone can see every certificate issued, in public logs such as
[crt.sh](https://crt.sh). The same is true of whoever runs your own
domain's DNS.

### Restoring without the old server

If your controller is lost, a fresh install can restore the lab from
Roostack Cloud with only your recovery key. For that, your lab gives
Roostack Cloud a public key worked out from the recovery key, and proves
it has the key by signing with it. Roostack Cloud checks those signatures
but can't work out your recovery key from them or open a backup with them
(SPEC §6.9, "Restoring from Roostack Cloud").

## Watching it yourself

### The request log

Every request your lab makes to Roostack Cloud is in **Settings → Roostack
Cloud → Details → Recent requests to Roostack Cloud**: when, what, what it
sent, and the answer. Secrets (the link's secret, signatures) show as "(not
recorded)", and anything Roostack doesn't know to be safe is left out the
same way. Long values, such as a certificate request, are shortened.

??? note "For technical users: the file"

    `/var/lib/roostack/cloud/requests.log`, one JSON object a line, the
    latest 200 (repeats of the same request with the same answer are one
    line, with a count):

    ```bash
    sudo tail -n 20 /var/lib/roostack/cloud/requests.log
    ```

    ```json
    {"At":"2026-10-19T02:00:04Z","Method":"PUT","Path":"/v1/lab/report","Sent":"{\"backups\":[],\"emails\":[\"sam@example.com\"],\"name\":\"Smith family\"}","Status":200,"Error":""}
    ```

    It holds what was sent, which includes the email addresses of the
    people who get problems by email and the addresses CrowdSec banned. It
    never holds Cloud's answers (storage keys come back in them). Not in
    it: what Caddy writes to Cloud's challenge server at each renewal, and
    restic's traffic to Cloud's storage. Requests to the challenge server
    and to Roostack Cloud's records API show their whole address; their
    login is in headers, which aren't recorded.

### The server's log

Roostack writes what it does to the system log: backups, updates, changes
to DNS, linking to Roostack Cloud and its certificate.

```bash
sudo journalctl -u roostack -u roostack-agent --since today
sudo journalctl -u roostack | grep "Roostack Cloud"
```

### The network itself

To see every connection your server makes, whatever made it, watch its
traffic. These show the server's connections now, and then each new
connection and DNS question as it happens:

```bash
sudo ss -tunp state established
sudo apt install tcpdump
sudo tcpdump -n -i any 'udp port 53 or (tcp[tcpflags] & tcp-syn != 0 and not dst net 10.0.0.0/8 and not dst net 192.168.0.0/16)'
```

Your router may also show which addresses a device talks to. Your server
is busiest at night, with backups and updates from 02:00.

## Checking a backup is encrypted

You don't need Roostack to check: restic, the program that makes the
backups, can do it, as can a look at the files themselves.

1. Find the backup: on the Backups page, **Manage**, then **Where it is**.
    On a drive it's a folder like
    `/mnt/roostack-backup/roostack/lab-…/n-…`.
2. Look at its files: they're random bytes, and something you know is in
    your data isn't there. For a drive:

    ```bash
    cd /mnt/roostack-backup/roostack/lab-…/n-…
    sudo head -c 64 config | od -c
    sudo grep -r -l "a word from one of your documents" . || echo "not found"
    ```

3. Open it with restic and the recovery key (on the controller), and
    see it's restic's format 2, that the only keys are the ones Roostack
    added, and what's in it:

    ```bash
    repo=/mnt/roostack-backup/roostack/lab-…/n-…
    key=/var/lib/roostack/backup-key
    sudo -u roostack restic --no-cache --no-lock -r $repo --password-file $key cat config
    sudo -u roostack restic --no-cache --no-lock -r $repo --password-file $key key list
    sudo -u roostack restic --no-cache --no-lock -r $repo --password-file $key snapshots
    ```

    With another key, restic will say "wrong password or no key found".

??? note "For technical users: other places"

    - **Online storage** also needs the account's keys, in
      `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (`export` them rather
      than putting them on the command line, where other users could see
      them), and `-r s3:…` as **Where it is** shows it.
    - **Roostack Cloud:** the storage keys are in
      `/var/lib/roostack/cloud-backup-keys.json` (`access_key_id`,
      `secret_access_key`, `session_token`, `region`, and `repository`), for
      12 hours at most: export them as `AWS_ACCESS_KEY_ID`,
      `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, and
      `AWS_DEFAULT_REGION`.
    - **Another server's backups** open with the recovery key too (each has
      the recovery key and the server's own). On that server,
      `/var/lib/roostack/backup-key` is its own key, which opens only its
      own backups.
    - Use only commands that read (`cat`, `key list`, `snapshots`, `ls`,
      `restore`): Roostack cleans up backups itself, weekly.
      `roostack backup snapshots` lists them the way Roostack sees them.

## Checking Roostack itself

The program checking all of this is Roostack, so it's checkable too:

- **Signed:** every release is signed with Roostack's release key, and
  your server checks each update against it before installing it.
- **Reproducible:** building a release's published source gives exactly
    the same files, byte for byte. Anyone with the source can rebuild a
    release and compare it with the one published, in one command (it needs
    Podman or Docker and takes about ten minutes):

    ```bash
    make verify-release VERSION=0.1.0
    ```

    It checks the release's signature, builds it from its source in the
    same environment the release was built in, compares every file, and
    says plainly whether they match. The source isn't public yet: until it
    is, only people with access to it can.

??? note "For technical users"

    The release key's fingerprint is published on roostack.cloud; the
    command shows the fingerprint of the key it checked with, to compare.
    How a release is built, signed, and checked, and the key's ceremony,
    are in `docs/RELEASING.md` in Roostack's source (SPEC §6.2.1).
