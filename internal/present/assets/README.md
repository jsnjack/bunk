# Assets of bunk present

These files are embedded in bunk and served to the pages that
`internal/present` renders, so a page loads nothing from the network.
`bunk-doc.js` and `bunk-doc.css` are bunk's own. The others are pinned
upstream builds, both under the MIT licence.

| File | Source | Licence |
|------|--------|---------|
| `mermaid.min.js` | Mermaid 11.4.1, <https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js> | `mermaid.LICENSE`, from <https://cdn.jsdelivr.net/npm/mermaid@11.4.1/LICENSE> |
| `katex/katex.min.js` | KaTeX 0.16.11, <https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.js> | `katex/LICENSE`, from <https://cdn.jsdelivr.net/npm/katex@0.16.11/LICENSE> |
| `katex/katex.min.css` | KaTeX 0.16.11, <https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.css>, changed as below | `katex/LICENSE` |
| `katex/fonts/*.woff2` | KaTeX 0.16.11, `https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/fonts/<name>.woff2` | `katex/LICENSE` |

KaTeX's stylesheet lists each font as woff2, then woff and ttf. Only the
woff2 files are bundled, and the woff and ttf entries are removed from the
stylesheet, so a browser never asks for a file that is not there:

```sh
sed -E 's/,url\(fonts\/[A-Za-z0-9_-]+\.woff\) format\("woff"\)//g;
        s/,url\(fonts\/[A-Za-z0-9_-]+\.ttf\) format\("truetype"\)//g'
```

SHA-256 of the upstream files:

```
a43bc1afd446f9c4cc66ac5dd45d02e8d65e26fc5344ec0ef787f88d6ddb6f9e  mermaid.min.js
e6bfe5deebd4c7ccd272055bab63bd3ab2c73b907b6e6a22d352740a81381fd4  katex/katex.min.js
717bc9ae7853b61f0f76455dddf0ecd4f527a783f42de2ac24684899c1c46258  katex/katex.min.css (before the change)
```

To update a library, download the new version's files from the same
paths, apply the change to the stylesheet, fetch the woff2 fonts it names,
and update the versions and hashes here. `TestAssetsReferenced` checks that
every font the stylesheet names is bundled.

## Code inside the Mermaid bundle

`mermaid.min.js` bundles third-party libraries. Its closing comment,
"Bundled license information", keeps their notices: DOMPurify 3.2.1
(Apache-2.0 or MPL-2.0), js-yaml 4.1.0, lodash-es and cytoscape (MIT). It
also contains d3 modules (ISC) and smaller MIT libraries such as dagre,
khroma, marked and dayjs. bunk-gui's RPM licence tag, MIT AND MPL-2.0 AND
ISC AND BSD-3-Clause, covers each of them; DOMPurify is taken under
MPL-2.0. Check the comment again when updating Mermaid.
