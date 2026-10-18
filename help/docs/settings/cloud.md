# Roostack Cloud

Roostack Cloud is an optional online service for Roostack labs. Your lab
works without it. Linking the lab to a free Roostack Cloud account gives it
a **free roost.ac address** (such as `smiths.roost.ac`), so you don't need a
domain of your own. An account with Roostack Cloud storage (in the pilot,
for now) can also keep your backups away from home, encrypted before they
leave, and later Roostack Cloud will check your lab from the internet:
each one your choice.

You'll find it under **Settings → Roostack Cloud**, or in the setup
wizard's address step.

## Linking your lab

1. Choose **Link to Roostack Cloud** (or **Continue to Roostack Cloud**,
   with the name you'd like). Roostack Cloud opens in the same tab.
2. Sign up or log in there, check it names your lab, then confirm. Roostack
   Cloud brings you back to your lab.
3. Your lab's page shows the steps as they finish, then moves on by itself.
   The link expires after 10 minutes; if it does, start again.

No password is typed into your lab, and nothing secret is copied by hand.

If you chose a name, your lab then sets it up as its address by itself
and restarts. A lab that's already linked gets a name straight away:
choose **Use this address** in **Settings → Your lab's address**.

## Your roost.ac address

Your lab's services are at `photos.smiths.roost.ac`, and Roostack itself
at `smiths.roost.ac`. Roostack Cloud runs the name's DNS; your lab tells
it where the name points (your server's home-network address, and your
home's internet address for services you make public), and gets its
certificates from Let's Encrypt through it.

For that, your lab gets a login from Roostack Cloud that can only change
your name's records. It's kept as Podman secrets
(`roostack-caddy-dns-*`), like the login for an own domain's DNS
provider. Roostack Cloud never has your certificates or their keys.

!!! warning "Only confirm a link you started"
    If someone sends you a Roostack Cloud link to confirm, don't: it would
    link *their* lab to *your* account. Only confirm a code your own lab is
    showing you right now.

## What your lab sends, and keeps

When it links, your lab sends Roostack Cloud its name, the roost.ac name it
asks for (if any), Roostack's version, a public key, and the address of
its own page to come back to (as your browser reached it). Nothing else: no
email address, and no list of your services.

Your lab keeps a **certificate** for that key, and uses it to prove who it
is each time it talks to Roostack Cloud: once a day and when Roostack
starts, and for what you use (a name, backups there, the relay); [Your
data and your keys](../your-data.md#what-roostack-sends) lists every
request. The private key never leaves your server. The certificate renews
itself with a new key.

For technical users, **Details** on the page shows the certificate's
fingerprint and dates, Roostack Cloud's address, and the lab's recent
requests to it: when, what, what it sent (with secrets left out) and the
answer. [Your data and your keys](../your-data.md#what-roostack-sends)
says what each request is for, and how to watch them yourself. The files
are in `/var/lib/roostack/cloud/`:

| File | Holds |
|---|---|
| `lab.key` | The lab's private key (readable only by Roostack) |
| `lab.crt` | Its certificate from Roostack Cloud |
| `link.json` | The account, since when, the last contact, and what Roostack Cloud holds back |
| `requests.log` | The latest 200 requests to Roostack Cloud, one a line, with what each sent (never a secret) |

They're part of Roostack's state, so they're in your backups: a lab you
restore is still linked.

## What your lab tells Roostack Cloud each day

After it checks in each day, your lab tells Roostack Cloud its name (as it
is now, in Settings → Lab name) and two more things, and nothing else:

- **The email addresses of the people here who get the lab's problems**
  (each person's address on their account page, if they get **Problems**:
  see [Email](email.md)). Roostack Cloud's emails about your lab then go to
  them too, not only to the account's Owner: a name approved or refused, a
  suspension, the lab not heard from for days, its recovery key used.
  Roostack Cloud first emails each address once, asking whether it may;
  nothing more goes to an address until its owner says yes. Each email
  after that says why that person is getting it, with a link to stop them.
  Roostack Cloud keeps 10 addresses per lab.
- **Each server's last backup to Roostack Cloud**, if it backs up there,
  with the server's name, so your lab's page on Roostack Cloud can show
  it.

**Emails to** on the page lists each address and how it stands: getting
Roostack Cloud's emails, not confirmed yet (and when Roostack Cloud emailed
the link), turned down, or past the 10 it keeps. **Settings → Email** says
the same under each person. **Details** says when your lab last told
Roostack Cloud. Someone who stops getting Problems emails, or removes their
address, drops off at the next day's report.

## When Roostack Cloud holds something back

Roostack Cloud tells your lab what it's holding back, and **Settings →
Roostack Cloud** and Home show it:

- **A name waiting for approval:** a name that looks like a bank's, a
  government service's or a well-known brand's (`paypal.smiths.roost.ac`)
  waits until a person at Roostack Cloud checks it, within 7 days. Until
  then it isn't published or relayed.
- **A name that wasn't approved:** it doesn't work, with the reason. Choose
  another name for that service.
- **A name or the whole account suspended,** after an abuse report: with
  the reason. A suspended name is out of DNS; a suspended account's names
  don't work and the relay is off, though your lab itself carries on and
  stays linked. Roostack Cloud emails the account's Owner how to appeal.

While something waits on Roostack Cloud, your lab checks every hour instead
of once a day, so it shows the answer soon after.

## Unlinking

**Unlink from Roostack Cloud** tells Roostack Cloud and deletes the lab's
key. If your lab's address is a roost.ac name, it stops working at once
and your lab goes back to having no address, so your services can't be
reached until you give it another. The name
is kept for your account for a year. If public visitors come in through
Roostack Cloud's relay, the relay is turned off too: your public services
can then be reached only if your router sends visitors to your server (see
Public access). If Roostack Cloud can't be reached at
the time, the lab is unlinked anyway: remove it on your account's page too.

## "No longer linked"

If Roostack Cloud stops accepting your lab's certificate, your lab forgets
the link and the home page says so. That happens when:

- the lab was removed from your account on Roostack Cloud;
- you restored the lab from a backup made before it was unlinked; or
- the lab was switched off for so long that its certificate expired.

Link it again if you still want it. If Roostack Cloud just can't be reached
(your internet connection, or Roostack Cloud itself), the link is kept: the
page says when it last got through, and the home page mentions it after
three days.

## Turning Roostack Cloud off

For a lab that should never contact Roostack Cloud, add this to
`/var/lib/roostack/lab.yaml` and restart Roostack:

```yaml
cloud:
  disabled: true
```

Nothing about Roostack Cloud is offered then. This is deliberately only in
the file, not in Settings.
