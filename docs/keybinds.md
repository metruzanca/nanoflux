# Keybinds

nanoflux keeps its global keybindings deliberately small. This page documents
the current set. Add new bindings here when they are implemented.

The source of truth is `internal/web/static/app.js`. The settings page renders
the same list from `shortcutsTable` in `internal/httpapi/views_layout.templ`, so
update both when a binding changes.

## Global bindings

| Key | Action | Context |
| --- | --- | --- |
| `←` / `→` | Previous / next item | Item preview is open |
| `Ctrl+P` / `⌘P` | Open the command palette | Anywhere |
| `Esc` | Close the active dialog, menu, or picker | Anywhere |

## Item preview

- `←` and `→` move through the rows in the current list.
- At the end of the loaded rows, `→` fetches the next page and continues into
  it, so reading does not stop at a page boundary.
- The modal pager buttons and horizontal swipe use the same navigation.
- Navigation pauses while a YouTube embed is open so the player keeps the arrow
  keys. Other embed players do not block item navigation.

## Command palette

- `Ctrl+P` / `⌘P` opens the command palette anywhere in the app.
- Inside the palette, `↑` / `↓` move through the results, `Enter` runs the
  selected command, and `Esc` closes the palette.
- The palette lists navigation targets and common actions. It is the only
  command entry point with a global keybinding.

## Removed bindings

There are no other global shortcuts. The previous row-cursor and item-action
keys (`j`/`k`, `o`/`Enter`, `v`, `s`, `m`, `g`/`G`, `ArrowUp`/`ArrowDown`),
`/` search focus, `?` help, and `Ctrl/Cmd+Shift+P` entity search were removed
to leave a clean slate for rebuilding the keymap.
