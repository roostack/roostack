MyLab Plan
===

MyLab is a system for installing and managing commonly used home lab services. It's designed for users to take control of their data by letting them easily self-host a range of useful services instead of relying on typical cloud providers.

It allows users to host entirely on their own local hardware, on a VPS, via the MyLab shared hosting platform, or a combination of them (using the master/worker setup).

Services are deployed as containers using podman, with a "mylab" orchestration service that handles management (via a web interface) and deploying other services. (The orchestration service runs on the host, not in a container; see SPEC.md §6.1.) The mylab orchestration layer is capable of running in a master/worker setup, where the primary mylab instance provides management control, and worker instances facilitate deploying to different targets.

Technology choices and development practices should favour simplicity over complexity. The project needs to be understandable and maintainable. Strong documentation should be generated in README.md and ARCHITECTURE.md. 

During development, we should prototype small sections and build things out slowly and logically. Make sure each bit makes sense and will fulfil long term goals before moving on or getting too complex.

## Rules

- Always stop and ask if you're unsure about anything - never assume, but make suggestions
- Regularly pause for the user to validate progress, especially between tasks
- Be concise, and avoid verbose cyclic thinking
- If you can't do things (like view the app running), ask the user for input

Understand this PLAN.md file, and should keep track of ongoing tasks in a TASKS.md file. Tasks can be extended and broken up into sub-tasks. You should begin building a SPEC.md that will include any locked in decisions (or pending ones) as well as rational for decisions made. 

## Initial decisions to make

- MyLab runs on Linux (and possible other platforms), but what distro's do we support by default
    - Distro's should support podman easily, as well as any extensions we might need (such as to bridge cpu/gpu acceleration into containers)
- What reverse proxy should we use? Should we allow the user to choose?
    - The management interface will configure the proxy directly, including support for private (local only or via VPN/tailscale), or publicly accessible
    - It must handle SSL (via Let's Encrypt, or something else suitable)
    - We may need for the user to be able to provide custom configuration
- How do we handle security?
    - Fail2ban? Crowdsec? What else?
- What language(s) and frameworks should we use for the main MyLab service?
    - We can use good quality dependencies, but every decision should be made collaboratively and for maintainability.
- Should we choose a default/standard SSO platform (such as Authentik)?
    - Should we force users to use this? Or make it optional?
- How should we handle databases? Push people towards single instances? Or multiple?
- How should we handle data volumes? Needs to be easily and automatically managed.
- How should we handle backups? This needs to be built in default behaviour, both of MyLab configuration data, and service specific data (including databases).
- Should we have a default DNS manager (such as PiHole) with automatic local configuration of DNS (for external domains that resolve internally)?
- Should we have a built-in uptime monitor (like Uptime Kuma)?


This is a starting point, there will be many more decisions to make.

## Supported Services

MyLab will initially support a small set of services with strong one-click management. Longer term, more services will be added and methods for the user to load their own services will be provided, either directly, or potentially via a community hub.

A non-exhaustive list of good starting services is:

- Immich
- Forgejo
- Home assistant
- Outline
- Papra
- Pihole
- Plex
- Jellyfin
- Vaultwarden
- wg-easy
