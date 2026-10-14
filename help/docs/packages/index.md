# Make your own service

Roostack installs services from **packages**: a file that says how to run
one app well inside Roostack. Its containers, where its data lives, its
databases, its web address and its settings. Roostack generates the rest:
the containers' units, the web server config, secrets, folders and the
backup plan.

If the app you want isn't in the list of services, you can write its
package yourself. It's a YAML file, usually under 100 lines, and the
three in Roostack's [service registry](https://github.com/roostack/services)
make good starting points.

This section is for technical users: you'll need to be at home with
containers, YAML and a terminal.

## Just for your lab

1. [Write the package](write.md), starting from one in the registry.
2. Check it with `roostack validate -local` (see
   [Check it](write.md#check-it)).
3. Add it to your lab with [Add your own](../services/add.md#your-own),
   and install it like any other service.

Nobody reviews a package you add yourself, so it can do things a
registry package can't (an image that isn't pinned, say). Roostack lists
those before installing it.

## For everyone

A package in the registry can be installed by every Roostack lab, so it
goes through more:

1. [Write the package](write.md), following the
   [package policy](../reference/packages.md#what-a-package-may-not-do).
2. [Try it on a lab](try.md): install it, use it, back it up, restore it
   onto a fresh server and remove it, noting what you did.
3. [Submit it](submit.md) to the registry as a pull request, with what you
   tested. A maintainer reviews it, and once it's merged, every lab can
   install it within a few hours.

The exact format of every field is in the
[package format reference](../reference/packages.md).
