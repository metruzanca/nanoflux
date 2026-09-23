## Project Rules

- Commit messages must follow the Conventional Commits spec.
- Document all environment variables in the readme.
- App links are internal by default, ↗ on every external link.
- mise is for development, make is for selfhosting an instance

# Notes

htmx 2.x does not swap `4xx`/`5xx` response bodies by default. `app.js`
(loaded by `views_layout.templ`) installs a global `htmx:beforeSwap` listener
that sets `shouldSwap = true` for any `status >= 400`, so error fragments
actually render. Do not remove or narrow that listener; every form/fragment
handler below relies on it.

`event.detail.successful` remains `false` on `4xx`/`5xx`, so `hx-on::after-request`
handlers that close dialogs on success (`if (event.detail.successful) ...`)
leave the dialog open on error — which is what lets the user read the message.
