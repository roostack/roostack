# Monitoring

Every minute, Roostack checks that each of your services answers at its
address, and tells you when one stops. **Monitoring** in the sidebar shows
every check: whether it's up now, its uptime over the last 30 days, and a
bar for each day.

## What's checked

Nothing to set up: Roostack checks by itself

- **each service's address**, as a browser would ask it. It's up if it
  answers within 10 seconds, with a valid certificate and without an error.
  A login page counts as up.
- **each of the lab's other servers**, over the lab's own network.
- **the lab's address**: that devices at home find your server at it.

A service you stopped, or one that's being installed, updated, or restored,
is **paused**: it isn't counted as down.

Roostack checks from inside your lab. So it can't tell whether your router
lets the internet in, and if the server running Roostack is off, nothing is
checked.

## When something stops answering

After three failed checks in a row (so one slow answer doesn't count), the
service will show as **Down**, under "needs attention" on Home, and on
Monitoring with what went wrong. If it lasts a few minutes, Owners and
Admins will be emailed, as for other problems (your account's
**Notifications** say which emails you get). When it answers again, the
email will say so.

Roostack also warns when a service's certificate is close to expiring:
Caddy renews certificates well before then, so it means renewing is
failing.

## A check's details

**Details** beside a check shows its last 90 days, its uptime over a day, a
week, a month, and three months, how long its answers take, the times it
was down and what came back, and exactly what's asked.

Each service's page also has an **Uptime** card.

## Your own checks

To know about something else on your network, such as your router, a NAS,
or a printer, choose **Add a check…** under **Your own checks** and give

- a **name**, and
- an **address**:
    - a web address (`https://nas.local:5001`): up if it answers without an
      error. Many routers and NASes have their own self-signed certificate:
      tick **Don't check its certificate** for those;
    - a name or IP address (`192.168.1.1`): up if it answers a ping;
    - one with a port (`printer.local:631`): up if something listens there.

Untick **Tell me when it stops answering** for one you only want to see.
To remove a check, open its **Details**.

## The public status page

An Owner can turn on a public status page at `status.` and your lab's
address, for the people who use your services to see whether they're
working. It shows only what you tick: each one's name, whether it's up,
and a bar of its last 90 days. Never an address or what went wrong.

Services open to the internet are ticked to begin with. Services only on
your home network aren't: showing them would tell everyone you run them.

The page is a plain file Roostack writes every minute; nothing on it runs
code.

## Turning it off

**Turn monitoring off**, at the bottom of the page, will stop the checks
and their emails. Services' own health checks and the rest of "needs
attention" will carry on.

## Where things are

| What | Where |
|---|---|
| Every result of the last two days, then each hour's summary | The controller's database, `/var/lib/roostack/roostack.db`, kept as long as **History** keeps things |
| Where checks run from | The lab's controller, asking its own Caddy (`127.0.0.1:443`) with each address's name |
