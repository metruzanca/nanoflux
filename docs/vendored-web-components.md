# Vendored web components (Vaadin)

nanoflux has no frontend build step and no npm at build or run time. Its few
JavaScript dependencies are **vendored**: a generated file is committed under
`internal/web/static/` and served like any other static asset (the directory is
`go:embed`-ed; see `internal/web/templates.go`).

This document covers the Vaadin web components used for form controls: where the
bundle comes from, how to regenerate it, how the server talks to the components,
and how to add more of them.

Status: Vaadin **25.3.0**, vendored as `vaadin-combo-box`,
`vaadin-multi-select-combo-box`, and `vaadin-grid`.

## Why vendored, not a bundler

Vaadin ships a tree of `@vaadin/*` ES modules that import each other by bare
specifier (`@vaadin/component-base`, `lit`, …). A browser cannot resolve those,
so the package folder cannot be copied in as-is. Bundling that closure once at
development time and committing the result keeps the runtime simple: no
`node_modules`, no build step, and self-hosting (the `make` path) stays
`go build`.

The bundle is **emitted as an IIFE, not ESM**, and loaded with a plain
`<script defer>`. A classic script containing `export` fails to parse, which
silently leaves every element unregistered — they then render as zero-size
unknown elements. Do not change the format without also changing the script tag.

Current size: ~237 KB minified, ~55 KB gzipped, no dynamic imports.

## Regenerating the bundle

The pinned versions live in `tools/vaadin-vendor/vendor.sh`. Regenerate with:

```bash
mise run vendor:vaadin      # needs node once; not needed at runtime
```

That script:

1. installs the pinned `@vaadin/*` packages and `esbuild` into a temp dir,
2. bundles `tools/vaadin-vendor/entry.js` (the list of components to include)
   into one minified IIFE at `internal/web/static/vaadin.bundle.js`,
3. prints the output size.

Commit the regenerated file, exactly as `htmx.min.js` is committed. Bump
`VAADIN_VERSION` in the script when upgrading; also bump `ESBUILD_VERSION`
occasionally.

## What is in the bundle

`tools/vaadin-vendor/entry.js` is the single source of truth for which
components ship. It currently imports:

```js
import "@vaadin/combo-box";
import "@vaadin/multi-select-combo-box";
import "@vaadin/grid";
```

Everything else in the bundle (the overlay, scroller, item, input-container,
theme mixin, usage-statistics, …) is a transitive dependency of those.

## How the server uses the components

The components are client-side custom elements, so the server renders markup and
a small script wires them up. There are three moving parts.

### 1. Templ wrappers (`internal/httpapi/views_combo.templ`)

`comboSelect`, `comboMulti`, and `comboChips` render the element plus the
hidden native mirrors described below. Options are passed as `[]comboItem`
(`internal/httpapi/combo.go`).

Options are emitted **inline as JSON attributes**:

- `items` on every combo, and
- `selected-items` on multi-selects.

Lit JSON-parses Array-typed attributes by default, so no client-side hydration
is needed. `selected-items` must be the **full item objects**, not bare value
strings: the component labels a selection by looking up the item object, and a
bare value makes it fall back to the value string (chips showing `165` instead
of `Test Blog`).

v25 has no `<optgroup>` equivalent, so a grouped picker prefixes the group name
onto the label (`"Metru · Blog"`).

### 2. Form-submission bridge (`internal/web/static/vaadin.js`)

The components are **not form-associated**, and their own internal `<input>`
holds display text. htmx serializes a form with `FormData`, which would submit
the **label** for a single-select and **nothing** for a multi-select. So each
wrapper pairs the component with hidden native inputs:

- **single select**: one mirror `input[data-vaadin-target]` carries the value
  and any `hx-*` attributes (spread from the wrapper), so
  `hx-trigger="change"` on it behaves exactly as it did on the `<select>` it
  replaced.
- **multi select**: one mirror per selected value, all sharing a name, so the
  field submits as repeated params (`r.Form["collections"]`) like the checkbox
  group it replaced.
- **chips**: no mirrors; display-only, and a removed chip POSTs to the
  wrapper's `data-remove-url`.

The script is idempotent, waits for `customElements.whenDefined`, and re-runs
after `htmx:afterSwap`. Fragments injected with `fetch` + `innerHTML` (the
add-to-list dialog) fire no htmx swap event, so callers must invoke
`window.nanofluxReinitVaadin(target)` after injecting; see `static/app.js`.

### 3. Theming (`internal/web/static/app.css`)

v25 ships **only structural base styles** — there is no Lumo theme package in
the dependency tree. Components are themed with CSS custom properties from
`app.css`, mapped to nanoflux tokens (`--panel`, `--bg`, `--border`, `--fg`,
`--accent`).

Vaadin's base color tokens default via CSS `light-dark()`, which follows the
**OS** preference, not our `data-theme`. They are therefore mapped once on
`:root` (which inherits into every component's shadow DOM) so the components
follow the app theme. Component-specific overrides sit near the bottom of
`app.css` under the "Vaadin components (vendored)" heading.

### Grid (`vaadin-grid`)

`vaadin-grid` is used once: the pinned-collection list on `/settings`, where
rows drag to reorder (`views_home.templ`, `static/home-grid.js`). It is the one
component that needs real glue, because its columns render imperatively:

- Columns take a JS `renderer`, not declarative templates. The card emits one
  `<template id="settings-home-row-{id}">` per pinned collection holding the
  row's plain server markup, and `home-grid.js`'s renderer clones the matching
  template into the cell, then `htmx.process(root)` so the row's `hx-*`
  attributes bind. The grid's cell content is slotted from the light DOM
  (`vaadin-grid-cell-content`), so htmx can reach it.
- Reordering listens for `grid-drop` (from Vaadin's drag-and-drop mixin),
  computes the new id order, and `htmx.ajax('POST', …)`s it to the card's
  `data-reorder-url`. The server's `reorder` action rebuilds the pinned order
  from the posted `order` list, ignoring unowned/unknown ids and preserving any
  pin the client omitted. Note `grid-drop` carries only `dropTargetItem` /
  `dropLocation`; the dragged items are on **`grid-dragstart`**, so the handler
  remembers them when the drag begins.
- `all-rows-visible` makes the grid as tall as its content (no inner scroll).
- `rows-draggable` + `drop-mode="between"` enable row drag and between-row
  drops.

Note: pass htmx `values` as a **plain object** (`{ action: 'reorder', order: idList }`),
not a `URLSearchParams` — htmx's encoder iterates own keys, so a
`URLSearchParams` silently sends nothing.

## Bringing in another Vaadin component

1. Add its package to the `npm install` list in
   `tools/vaadin-vendor/vendor.sh` **and** an `import "@vaadin/<name>";` line to
   `tools/vaadin-vendor/entry.js`.
2. Run `mise run vendor:vaadin` and commit the regenerated bundle.
3. Add a templ wrapper in `views_combo.templ` (or a new `views_<name>.templ`),
   following `comboSelect` as the model. Decide whether it needs a mirror:
   - If it is a **form control** you submit, it needs mirrors, plus a branch in
     `static/vaadin.js` (`data-vaadin="<kind>"`) to sync them.
   - If it is **display-only** or driven entirely by htmx attributes it carries
     itself, it may need no JS at all.
4. Theme it in `app.css` with its `--vaadin-*` properties.
5. If it is a plain fixed-set choice where search adds nothing (2–3 options),
   prefer the existing custom pill picker (`PickerControl` in
   `views_items.templ`) — see `/settings` → home screen for an example.

### Gotchas

- **Check for a theme package.** Some components used to pull Lumo styles via
  `@vaadin/vaadin-lumo-styles`. v25 does not, but verify for any new one; if it
  does, add that import too or the component renders unstyled.
- **Opt out of usage statistics.** `@vaadin/component-base` imports
  `vaadin-usage-statistics`, which only runs in development mode (localhost, and
  only when not minified). The production bundle is minified, so it is inert;
  note this if you ever ship an unminified bundle.
- **`items`/`selected-items` are JSON attributes.** Prefer them over setting
  properties from JS, so the markup is complete without hydration.
- **Form participation.** Assume a new component is not form-associated until
  proven otherwise; test `new FormData(form)` for the value you expect.
- **Component internals can be shadow DOM.** Grid cell content happens to be
  slotted (light DOM) so htmx can reach it, but do not assume that of other
  components; check where their interactive content lands.
- **`htmx:afterSwap`'s `detail.target` may be detached** after an `outerHTML`
  swap. The init hooks scan the whole document when the target is disconnected,
  so re-initialization is not skipped.

## Where this lives in the code

| Concern | File |
| --- | --- |
| Pinned versions + bundle command | `tools/vaadin-vendor/vendor.sh` |
| Which components ship | `tools/vaadin-vendor/entry.js` |
| Committed bundle (generated) | `internal/web/static/vaadin.bundle.js` |
| Committed bridge script (combos) | `internal/web/static/vaadin.js` |
| Committed renderer/reorder script (grid) | `internal/web/static/home-grid.js` |
| Templ wrappers | `internal/httpapi/views_combo.templ`, `views_home.templ` |
| Option/data helpers | `internal/httpapi/combo.go`, `home.go` |
| Theming | `internal/web/static/app.css` (Vaadin section) |
| Load order (htmx, bundle, bridge, app) | `internal/httpapi/views_layout.templ` |
