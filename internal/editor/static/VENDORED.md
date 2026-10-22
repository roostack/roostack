# Vendored JavaScript (the package editor)

Served from the roostack binary and copied to roostack.cloud with the editor: no CDN, so the editor works offline (SPEC.md §8.3.7, "The package editor").

| File | Source | SHA-256 |
|---|---|---|
| yaml-2.9.1.min.js | [yaml 2.9.1](https://www.npmjs.com/package/yaml/v/2.9.1) (ISC, Eemeli Aro; npm integrity `sha512-3NxN8+78OdzbT7C/WjGsyfPAtJaN3FNDsWxv7Y7mcDsT/oOmgW8BpyQQFFBnvZE3j9Y2Sdz1ULFLezL7Eb2yFw==`), bundled with esbuild 0.28.2; its licence is the comment at the top | 339a43ad356fbd1c09026ac932685dc1a8a93c39ef77b142ef872ab5f66c59bc |

yaml ships only as ES modules, and a page opened from a file can't load modules, so it's bundled once into one plain script that sets `YAML`. To make it again (or update it), in an empty folder outside the repository:

```sh
echo '{"private":true,"dependencies":{"yaml":"2.9.1","esbuild":"0.28.2"}}' > package.json
npm install --ignore-scripts && node node_modules/esbuild/install.js
echo 'export * from "yaml";' > entry.js
node_modules/.bin/esbuild entry.js --bundle --format=iife --global-name=YAML --platform=browser \
  --target=es2020 --minify --charset=utf8 --outfile=yaml.js
```

then put the comment on top (the first line, then `node_modules/yaml/LICENSE` with ` * ` before each line, then ` */`) and `yaml.js` below it. The bundle on its own has SHA-256 `042ea9c537bb9524b267cf773a1214d17f45b16c2b650ff08f947e11162f4b77`, the same each time. Update this table, the file name in `scripts.json` (its `scripts` and `vendored`) and THIRD_PARTY.md (`internal/web/credits.go`).
