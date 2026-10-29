# Vendored web components and JS

nanoflux has no frontend build step at build or run time. Its JavaScript and the
component CSS are **generated in development and committed**: the files live
under `internal/web/` and are served like any other static asset (the `static`
directory is `go:embed`-ed; see `internal/web/templates.go`). The Docker build
only runs `go build`.

This document covers the copied shadcn-templ components used for form controls
and the committed outputs, plus how to regenerate them. `htmx.min.js` is
vendored the same way; hls.js is covered at the end.

## shadcn-templ

Status: shadcn-templ **v2.0.0-beta.16**, style `base-nova`, Tailwind CSS
**4.3.3**.

The component source is copied into the repository with the shadcn-templ CLI,
under `internal/web/ui`:

- `internal/web/ui/combobox`, `switch`, `slider` are the components we use.
- `button`, `input`, `inputgroup`, `textarea`, `icon` and `utils` are their
  local dependencies.
- `internal/web/ui/baseui` and `internal/web/ui/floatingui` contain the shared
  browser runtime and positioning code.
- `internal/web/ui/scripts.templ` renders the one bundle script tag, and
  `internal/web/ui/scripts_bundle.go` is the generated URL manifest.

`components.json` (repo root) records the style, the Tailwind entry file, and
the aliases that keep everything under `internal/`. That last point matters: the
local source build copies only `cmd`, `internal` and `extension` into the image,
so the components build without changing `Dockerfile.local` or `make`.

### Generated outputs (committed)

| Output | Purpose |
| --- | --- |
| `internal/web/static/shadcn.css` | Minified Tailwind output |
| `internal/web/static/shadcn-templ-<hash>.js` | Component runtime bundle |
| `internal/web/ui/scripts_bundle.go` | Bundle URL used by `@ui.Scripts()` |
| `internal/web/ui/**/_templ.go` | Generated templ code for copied components |

Regenerate both the JS bundle and the CSS with:

```bash
mise run ui:gen
```

`tools/shadcn/build.sh` pins the CLI and Tailwind versions. It installs the
dev-only Tailwind packages into `tools/shadcn/node_modules` if they are missing,
runs `shadcn-templ bundle`, then compiles the Tailwind entry. `node_modules` is
not committed.

After adding or changing a `.templ` component, also run `mise run gen` (the
normal templ and sqlc generation). Commit the generated `_templ.go`, the CSS,
the new hashed JS file and `scripts_bundle.go`.

### Tailwind entry and theming

`tools/shadcn/tailwind.css` is the Tailwind source. It imports only Tailwind's
theme and utilities layers, plus the vendored `tw-animate.css` and
`shadcn-tailwind.css`. **Preflight is intentionally omitted**: `app.css` owns
the page base, and Tailwind's reset would change the hand-written UI. `app.css`
also scopes its generic button and input rules with `:not([data-slot])`, so the
hand-written form styles cannot override the shadcn utilities.

The theme maps the shadcn `@theme` color tokens directly to nanoflux variables:

| shadcn token | nanoflux value |
| --- | --- |
| `background` / `foreground` | `--bg` / `--fg` |
| `card`, `popover`, `secondary` | `--panel` |
| `primary` | `--accent` |
| `muted-foreground` | `--muted` |
| `border`, `input` | `--border` |
| `destructive` | `--danger` |
| `ring` | `--accent` |
| `radius` | `6px` |

The shadcn dark variant is mapped to the app's
`[data-theme="dark"]` selector, so light/dark and the accent setting keep
working without a second theme system.

### Form behavior

The components render their own hidden native inputs, so htmx and normal form
submits see the field values without a bridge script. There are a few rules:

- Use `DefaultValue`, `DefaultValues` and `DefaultChecked`. The controlled props
  (`Value`, `Values`, `Checked`) make the JS wait for an owner update and ignore
  the user.
- Field-level htmx attributes go on the wrapper through `comboFieldAttrs`
  (`internal/httpapi/views_combo.templ`). It adds `hx-disinherit="*"`, a
  `change from:` trigger that names the hidden input, and an `hx-include` that
  pulls the field's value while preserving the caller's selectors. This keeps
  typing in the search box from submitting, while a real selection still does.
- Some forms put `hx-trigger="change"` on the form itself (settings). The native
  `change` event that shadcn dispatches on its hidden input bubbles to the form
  and works unchanged.
- Components self-initialize through a MutationObserver, so htmx swaps, cloned
  filter rows and `fetch` + `innerHTML` dialogs need no reinit hook. Keep
  `htmx.process` for htmx attributes, but do not add a component reinit.

### Native dialogs

shadcn portals its popups to `<body>`. A native `<dialog>` lives in the top
layer, so a body-portaled combobox inside one would render behind it.
`internal/web/static/shadcn.js` redirects the portal into the nearest
`<dialog>` ancestor instead. It also turns the slider's drag events into one
native `change` on pointerup or keyup, so a surrounding form's
`hx-trigger="change"` fires once when the drag settles.

The filter modal renders one combobox per possible kind (`tag` and `feed`) in
`<template>` elements. `app.js` swaps the value cell when the kind select
changes, so no client-side option JSON or component API is involved.

### Plain chip list

The collection member list is not a combobox. `comboChips` renders ordinary
server markup: one chip per feed, with a remove button that POSTs to the
collection's `remove-feed` endpoint. Auto collections render no remove button.

### Home reorder

The pinned-collection list on `/settings` is plain server markup. Native HTML5
drag and drop in `internal/web/static/home-reorder.js` computes the new order
and posts it to the card's `data-reorder-url`. There is no grid component and
no imperative row renderer.

### Adding another component

1. Copy it from the pinned CLI:

   ```bash
   go run github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@v2.0.0-beta.16 add <name>
   ```

2. Run `mise run gen` and `mise run ui:gen`.
3. Add a wrapper in `views_combo.templ` (or a new `views_<name>.templ`). Use the
   uncontrolled props and the `comboFieldAttrs` helper when the control submits
   a form value on its own.
4. Check the Tailwind output includes the new classes, and commit all generated
   files.

Gotchas:

- The CLI rewrites imports to the aliases in `components.json`. Keep copied
  source under `internal/`; a root-level `components/` directory would need a
  Dockerfile change.
- `internal/web/ui/utils` uses `github.com/Oudwins/tailwind-merge-go`, which is
  a normal build dependency of the main module.
- Copies are MIT licensed. Keep the upstream license notice when redistributing
  them.
- Do not remove preflight exemption or the `:not([data-slot])` guards without a
  visual pass over the hand-written UI.

## hls.js (HLS video)

Some plugin videos are stored as HLS enclosures (an `.m3u8` manifest), for
example Bluesky videos, which Chrome and Firefox cannot play natively.
`internal/web/static/hls.light.min.js` is the pinned hls.js **light** build (VOD
playback; the full build is not needed), and
`internal/web/static/hls-video.js` is the small committed glue that attaches it
to any `video[data-hls]` (Safari is left to play HLS natively).

Regenerate the vendored build with:

```bash
mise run vendor:hls      # needs node once; not needed at runtime
```

`tools/hls-vendor/vendor.sh` installs the pinned `hls.js` version and copies
`dist/hls.light.min.js` (an IIFE that sets `window.Hls` when loaded as a classic
script). hls.js is **lazy**: `hls-video.js` injects the script only when a page
actually has an HLS video, so readers who never open one do not download it.

## Where this lives in the code

| Concern | File |
| --- | --- |
| CLI config and aliases | `components.json` |
| Pinned CLI, Tailwind versions and regeneration | `tools/shadcn/build.sh`, `tools/shadcn/package.json` |
| Tailwind entry and theme mapping | `tools/shadcn/tailwind.css` |
| Copied component source | `internal/web/ui/**` |
| Generated Tailwind output | `internal/web/static/shadcn.css` |
| Generated JS bundle and URL manifest | `internal/web/static/shadcn-templ-*.js`, `internal/web/ui/scripts_bundle.go` |
| Portal and slider glue | `internal/web/static/shadcn.js` |
| Home reorder | `internal/web/static/home-reorder.js` |
| Templ wrappers | `internal/httpapi/views_combo.templ`, `views_home.templ` |
| Option/data helpers | `internal/httpapi/combo.go`, `home.go` |
| Wrapper layout and chip styling | `internal/web/static/app.css` |
| Load order | `internal/httpapi/views_layout.templ` |
