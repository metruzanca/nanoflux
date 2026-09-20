# AGENTS.md

Project-specific guidance for coding agents working in this repository.

## Error handling in the web UI

The web UI is server-rendered with htmx. All user-facing failures must render a
visible error into the page. Never return a bare status or fail silently.

### htmx swap rules (load-bearing)

htmx 2.x does not swap `4xx`/`5xx` response bodies by default. `layout.html`
installs a global `htmx:beforeSwap` listener that sets `shouldSwap = true` for
any `status >= 400`, so error fragments actually render. Do not remove or narrow
that listener; every form/fragment handler below relies on it.

`event.detail.successful` remains `false` on `4xx`/`5xx`, so `hx-on::after-request`
handlers that close dialogs on success (`if (event.detail.successful) ...`)
leave the dialog open on error — which is what lets the user read the message.

### Two error response shapes

1. **Preview endpoints** (`/fragments/feed-preview`, `/fragments/author-preview`):
   the form's `hx-target` *is* the preview container, so errors replace it via a
   normal swap. Return `400` + the `form_error` fragment using `renderError(w, msg)`.
   Do NOT use OOB here — an out-of-band div targeting the same element as the
   normal target is fragile.

2. **Mutation forms** (`POST /feeds`, `/authors`, `/collections`): the target is
   the list; the error slot is a separate `#add-…-error` div inside the open
   dialog. Return `400` + an OOB swap into that slot using `writeFormError(w, target, msg)`.

Error messages are short, user-facing, and generic. Log the underlying cause
server-side with `log.Error(...)`; never leak internals (SQL, URLs, stack traces)
to the client. `5xx` responses currently render plain text into the target via
the global swap override; prefer returning a fragment there too.

### Error template

`form_error` lives in `internal/web/templates/fragments.html` and renders a
`role="alert"` banner. Keep the `role="alert"` so screen readers announce it.

### Tests

Every error path must assert both `rr.Code == http.StatusBadRequest` and that
the message (or the `form_error`/`role="alert"` fragment) appears in the body.
See `internal/httpapi/preview_test.go` and `internal/httpapi/web_test.go` for
the pattern.