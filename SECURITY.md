# Security

This policy covers both of the project's products: **Roostack**, the software in this repository that people run on their own servers, and **Roostack Cloud**, the hosted service at roostack.cloud that gives labs names on roost.ac, a relay and backup storage. Roostack Cloud's code is private, so reports about it come here too.

## Reporting a vulnerability

Please don't open a public issue for a security problem. Report it privately in either of these ways:

- **GitHub's private vulnerability reporting:** the **Security** tab of [roostack/roostack](https://github.com/roostack/roostack), then **Report a vulnerability**. This keeps the report, our replies and any fix in one private place.
- **Email** [security@roostack.cloud](mailto:security@roostack.cloud).

It helps to include what's affected (Roostack's version, or the Roostack Cloud page or API), what someone could do with the problem, and the steps to show it. If you've found it in Roostack Cloud, please also say when you tried it, so we can find it in our logs.

We'll reply to say we have your report, and we'll keep you told as we look into it and fix it. When the fix is out, we'll say so in the release notes, crediting you if you'd like that. Please give us a reasonable time to fix a problem before you tell anyone else about it, and tell us if you plan to publish something, so we can agree a date.

## What's in scope

- **Roostack:** `roostack` and `roostack-agent`, the installer (`scripts/install.sh`), the release and apt repository's signing, the service registry's index and its signatures, the packages in [roostack/services](https://github.com/roostack/services) as Roostack sets them up, the package editor, and the help site.
- **Roostack Cloud:** the website and its console at roostack.cloud, the API labs use, roost.ac names and their DNS, the relay and its edges, and Roostack Cloud backup.

Out of scope:

- Problems in the services Roostack installs (Immich, Vaultwarden and the rest): please report those to each project. Do tell us if Roostack's own setup makes one worse, or if a package should change because of it.
- Attacks on availability, such as flooding Roostack Cloud with requests, and social engineering of the people behind the project.

## Testing Roostack Cloud

Test only with your own account and your own labs. Don't read, change or delete anyone else's data, and stop as soon as you find you can; tell us what you saw. Don't run automated scanners that send large numbers of requests. A lab you run yourself, on your own hardware or a VM, is yours to test as hard as you like.

## Supported versions

Roostack hasn't had a release yet. Once it has, fixes will go into the latest release series (SPEC.md §7.2). Roostack Cloud is a service, so it's always the current version.

## Earlier reviews

Before the first release, Roostack and Roostack Cloud had a security review in October 2026: the root boundary, network trust, restore integrity and a set of smaller fixes. The fixes made in Roostack are listed in [TASKS-DONE.md](TASKS-DONE.md), under the "Security review" headings, and the decisions behind them are in [SPEC.md](SPEC.md).
