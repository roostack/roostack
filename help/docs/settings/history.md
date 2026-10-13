# History

**Settings → History** lists what happened in your lab, newest first:

- **What Roostack did** on each server: applying changes (with why they
  ran, such as "Installing Forgejo" or "Roostack started"), backups,
  updates, restores and moves. Choose **Log** to see everything it printed
  while it worked, as you'd see it live under **Details**.
- **What people did** in Roostack: logins, installs, settings changes,
  password and two-factor changes. A name of "system" means Roostack or
  the server's command line.

**Show** narrows the list to one kind. **Older** pages back.

Runs that changed nothing (most "applying changes" at start-up) aren't
kept: the latest is shown on **Settings → Servers**, under **Changes**.

## How long it's kept

Choose **Keep history for** 30 days, 90 days (the default) or a year.
Older history is deleted every day, on every server. Each server also
keeps at most 5,000 entries, and each log its last 2,000 lines.

**Delete all history** deletes everything now, on every server. The
deletion itself is recorded, so there's always a trace that it happened.
