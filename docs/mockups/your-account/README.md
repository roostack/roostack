# Your account: a section for everyone (2026-11-10)

**Asked by the maintainer (2026-11-10):** formalise a part of Roostack that isn't for running the lab, beside the sign-in page, where each person's own settings live. Its first use beyond what's there today will be WireGuard access for people, where each person adds their own devices. It's called **Your account** for now, and Owners and Admins manage their own account there too, so there's one place for everyone. The background is in the maintainer's services to-do (kept privately).

**Open them** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/your-account/index.html>. The tabs switch between the pages, and the button at the bottom right between light and dark. What's new is outlined with a dashed line. The pages are written by hand with the real stylesheet; `mock.css` adds only the section's header and the service tiles.

## What's there today

- **The sign-in page's home** at `login.<domain>` (SPEC §5): one narrow card saying hello, the services the person can use, a link to Roostack's pages when single sign-on is on, the photo migration when it applies, and Sign out.
- **Your account at `roost.<domain>/account`**, as tabs inside Roostack's pages: Your account (username, role, two-factor, change password), Two-factor, Email and notifications, and Appearance.
- **Members use Roostack's pages too:** Home and Services, without the parts for running the lab.

## The proposal

**One section for everyone, at the sign-in page's address**, in the lab's look (its name, logo, and colours, as the sign-in page has them), with a header showing who's signed in and tabs:

| Tab | What's on it | Where it is today |
|---|---|---|
| **Services** (`/`) | the services the person can use, as tiles; for Owners and Admins, **Run the lab**, which opens Roostack's pages; the photo migration when it applies | the sign-in page's home |
| **Account** (`/account`) | name and email address, which the person changes themselves; username, role, and groups, which only the Owner changes; light or dark | Roostack's Your account, and the email address from Email and notifications |
| **Security** (`/account/security`) | password, two-factor, and **where you're signed in**, with Sign out for each device and everywhere else (new) | Roostack's Your account and Two-factor |
| **Notifications** (`/account/notifications`) | which emails the person gets | Roostack's Email and notifications |
| **Devices** (`/account/devices`) | WireGuard devices, later; the tab will appear when the Owner turns WireGuard access on | new |

- **Roostack's pages at `roost.<domain>` become the Owner's and Admins' alone.** A Member who goes there will be sent to Your account. The sidebar's name and avatar will open Your account rather than Roostack's own account pages, which go.
- **Roostack's Appearance page stays with Roostack's pages:** its colours are each Owner's and Admin's choice for Roostack, while Your account takes the lab's look. Light or dark is one choice for both, on Account.
- **A lab without an address** has no `login.<domain>`, so Roostack will serve the same section at its own address (`/account` and the rest, beside its own login), with the same header and tabs.
- **A Member's notifications** list only the groups that apply to them (Security and the weekly summary), as Roostack's roles already filter what each person is told.
- **Safety:** the section can be public whenever the sign-in page is. Changing the password or two-factor already asks for the current password; adding a WireGuard device will ask for the two-factor code again (services to-do).

## Questions for the maintainer

1. **Layout:** a header with tabs, as mocked, or the single narrow card the sign-in page's home has today, with links to each page? *Recommendation:* the header and tabs, which give room for the tables (devices, where you're signed in) and match Your account's tabs today.
2. **Members and `roost.<domain>`:** always sent to Your account, as proposed? Then Members lose Roostack's Home and Services pages, which show little beyond what Your account's Services tab will. *Recommendation:* yes. The photo migration keeps its page of its own at `roost.<domain>`, as now (SPEC §8.5).
3. **Where you're signed in** is new: Roostack would keep each sign-in's browser and when it was last used. Build it with the section, or later? *Recommendation:* with the section, since the sessions are already kept and it's what someone wants after losing a phone.
4. **The tabs' names:** Services, Account, Security, Notifications, and Devices. Any you'd rather call something else?
