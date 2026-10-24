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
| `Ctrl+P` / `⌘P` or `Ctrl+Shift+P` / `⌘Shift+P` | Open the command palette and search | Anywhere |
| `Esc` | Close the active dialog, menu, or picker | Anywhere |

## Item preview

- `←` and `→` move through the rows in the current list.
- At the end of the loaded rows, `→` fetches the next page and continues into
  it, so reading does not stop at a page boundary.
- The modal pager buttons and horizontal swipe use the same navigation.
- Navigation pauses while an embed player is open so the player keeps the arrow
  keys (for example, a YouTube embed). Other embed players do not block item
  navigation.

## Command palette and search

- `Ctrl+P` / `⌘P` and `Ctrl+Shift+P` / `⌘Shift+P` both open the same palette
  anywhere in the app.
- On touch devices, swipe down from the topbar to open it.
- Inside the palette, `↑` / `↓` move through the results, `Enter` runs the
  selected command or opens the selected entity, and `Esc` closes the palette.
- The palette lists navigation targets and common actions, then the user's
  authors, collections, and feeds with an unread count for each row.
- Prefix the query with `@`, `#`, or `!` to search only authors, collections,
  or feeds. Without a prefix it searches all three (and the actions).

## Removed bindings

There are no other global shortcuts. The previous row-cursor and item-action
keys (`j`/`k`, `o`/`Enter`, `v`, `s`, `m`, `g`/`G`, `ArrowUp`/`ArrowDown`),
`/` search focus, and `?` help were removed to leave a clean slate for
rebuilding the keymap.
