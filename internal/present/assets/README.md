# Assets of bunk present

These files are embedded in bunk and served to the pages that
`internal/present` renders, so a page loads nothing from the network.
`bunk-doc.js` and `bunk-doc.css` are bunk's own. The others are pinned
upstream builds under the licences below.

| File | Source | Licence |
|------|--------|---------|
| `mermaid.min.js` | Mermaid 11.4.1, <https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js> | `mermaid.LICENSE`, from <https://cdn.jsdelivr.net/npm/mermaid@11.4.1/LICENSE> |
| `katex/katex.min.js` | KaTeX 0.16.11, <https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.js> | `katex/LICENSE`, from <https://cdn.jsdelivr.net/npm/katex@0.16.11/LICENSE> |
| `katex/katex.min.css` | KaTeX 0.16.11, <https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/katex.min.css>, changed as below | `katex/LICENSE` |
| `katex/fonts/*.woff2` | KaTeX 0.16.11, `https://cdn.jsdelivr.net/npm/katex@0.16.11/dist/fonts/<name>.woff2` | `katex/LICENSE` |
| `echarts.min.js` | Apache ECharts 6.1.0, <https://cdn.jsdelivr.net/npm/echarts@6.1.0/dist/echarts.min.js> | Apache-2.0, `echarts.LICENSE` and `echarts.NOTICE`, from the same package; it bundles zrender and parts of d3 under BSD-3-Clause, which the licence file lists |
| `viz-global.js` | Viz.js 3.31.0, <https://cdn.jsdelivr.net/npm/@viz-js/viz@3.31.0/dist/viz-global.js>: Graphviz 16.1.0 and Expat 2.8.5 compiled to WebAssembly | MIT (`viz.LICENSE`, from the repository at the release's commit `c8ce050d`); Graphviz EPL-2.0 (`graphviz.LICENSE`, from <https://gitlab.com/graphviz/graphviz/-/raw/16.1.0/LICENSE>, source at <https://gitlab.com/graphviz/graphviz>); Expat MIT (`expat.LICENSE`, from <https://github.com/libexpat/libexpat/blob/R_2_8_5/expat/COPYING>) |
| `d3.min.js` | D3 7.9.0, <https://cdn.jsdelivr.net/npm/d3@7.9.0/dist/d3.min.js> | ISC, `d3.LICENSE` |
| `highlight/highlight.min.js`, `highlight/github.min.css`, `highlight/github-dark.min.css` | highlight.js 11.12.0 with its common languages, <https://cdn.jsdelivr.net/npm/@highlightjs/cdn-assets@11.12.0/> (`highlight.min.js`, `styles/github.min.css`, `styles/github-dark.min.css`) | BSD-3-Clause, `highlight/LICENSE` |

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
b66b25aeb4df84e33199dc21694014d336d222cbd9deb0e5a7c14bd6aa0d0fd0  echarts.min.js
c9e0b310f9883910e01c66054b48b7ff4be3d8695141116635e72b0af152692e  viz-global.js
f2094bbf6141b359722c4fe454eb6c4b0f0e42cc10cc7af921fc158fceb86539  d3.min.js
8ab71eb09c51f501e5e25157d9cff100e46cc29bcbfc744d0b746d451fca7f53  highlight/highlight.min.js
3a9a5def8b9c311e5ae43abde85c63133185eed4f0d9f67fea4b00a8308cf066  highlight/github.min.css
9f208d022102b1d0c7aebfecd8e42ca7997d5de636649d2b31ea63093d809019  highlight/github-dark.min.css
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
khroma, marked and dayjs. bunk-gui's RPM licence tag, which names MIT,
MPL-2.0, ISC and BSD-3-Clause, covers each of them; DOMPurify is taken
under MPL-2.0. Check the comment again when updating Mermaid.

## Choosing versions

A library is pinned to a release at least two weeks old when it is added
or updated: D3 7.9.0 rather than 7.9.2, which was eight days old. The
runtime loads each library only for a page that uses it, and nothing else
reads them, so a library adds to the binary's size, about 2.9 MB for
ECharts, Viz.js, D3 and highlight.js, but not to a page's.

Graphviz runs as WebAssembly, so pages need `'wasm-unsafe-eval'` in their
`script-src` (`contentSecurityPolicy`); it allows compiling WebAssembly,
not evaluating JavaScript. bunk-gui's RPM licence tag names EPL-2.0 for
Graphviz and Apache-2.0 for ECharts.
