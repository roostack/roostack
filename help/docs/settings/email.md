# Email

Roostack can send email through your email provider's mail server (its
*SMTP* server). Set it up in **Settings → Email**.

## Setting it up

Your provider's help pages list its SMTP details, usually under "set up
mail in another app" or "SMTP". You need:

SMTP server
:   Such as `smtp.fastmail.com` or `smtp.gmail.com`.

Connection
:   **STARTTLS** on port **587** for most providers, or **TLS** on port
    **465**. Either way the connection is encrypted: Roostack never sends
    your password over a connection that isn't.

Username and password
:   Usually your email address, and an **app password**: a separate
    password your provider makes for one app, which you can cancel without
    changing your main one. The password is kept on the server, readable
    only by Roostack, and never shown again.

Send from
:   The address emails come from. Many providers only allow addresses you
    own, such as one on your domain.

Choose **Save**, then **Send a test email** to check it works. If it
doesn't arrive, look in your spam folder; if Roostack shows an error, it
says which step failed (reaching the server, the password, or the sender
address).

## Who gets email

Each person adds their own address on their account's **Email and
notifications** page (click your name at the top). Leave it empty to get
no email.

## Notifications

On your account's **Email and notifications** page, choose what Roostack
emails you about:

Problems
:   Backups failing, a disk running low, a service or server that stopped
    working, updates that failed. Sent once a problem has lasted ten
    minutes (a blip that fixes itself sends nothing), then once a day while
    it lasts, and once more when it's fixed.

Waiting for you
:   Something Roostack can't finish without you, such as a restart to
    finish installing updates, once it has waited a day.

Security for the lab (Owners)
:   A new Owner or Admin, someone's password or two-factor reset, a server
    joining the lab, another lab starting to store backups here.

Weekly summary
:   On Monday mornings: each server's backups and disk space, the attacks
    CrowdSec saw and blocked on your public addresses, and anything still
    needing attention.

Owners see who gets what at the bottom of **Settings → Email**. On a lab
linked to [Roostack Cloud](cloud.md), it also says whether each person
getting Problems has confirmed Roostack Cloud's emails about the lab.

Owners and Admins get the first three unless they change it; Members can't
act on the lab's problems, so they get none of them. Everyone always gets
security emails about their own account: a password or two-factor change,
a reset, a login from a new browser, or codes locked after wrong tries. If
one of those wasn't you, change your password and tell your lab's Owner.
