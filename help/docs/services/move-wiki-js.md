# Moving from Wiki.js 2 to 3

Wiki.js has two lines, and Roostack offers each as its own service:
**Wiki.js** is the 3.x line, for new wikis, and **Wiki.js 2** is the older
2.x line, for wikis already on it. Wiki.js has no upgrade from one to the
other, so Roostack won't move a Wiki.js 2 to 3 as an update. Wiki.js 2 will
keep getting its own updates.

To move, you'll use Wiki.js's own export and import. Both services can run
side by side while you check the result:

1. Back up Wiki.js 2 from its page in Roostack, so you can go back to it.
2. Update Wiki.js 2 to its newest version, then export a backup file
   (`.wkbackup`) in its administration area.
3. Install **Wiki.js** next to it, at another web address for now.
4. Import the file in the new wiki's administration area. Wiki.js's own
   [documentation](https://docs.js.wiki) says what the import brings across.
5. Check your pages. Settings for storage, search, and other modules don't
   come across, so set them again in the new wiki. Single sign-on is on the
   service's page in Roostack, as before.
6. Once you're happy, remove Wiki.js 2 and give the new wiki its address.
