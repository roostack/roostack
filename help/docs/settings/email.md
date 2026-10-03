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

Each person adds their own address under **Your email address** on their
account page (click your name at the top). Leave it empty to get no email.
