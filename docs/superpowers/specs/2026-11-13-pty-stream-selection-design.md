# Pty stream selection: design

Status: approved approach (2026-11-13). The question was how to get output out of the capture
screen; terminal-emulator mode was rejected, mouse drag-select with copy accepted. Base: incoda
0.10.3.

## 1. Summary

The capture screen gains text selection: drag inside the pane selects cells, releasing copies
them, the selection paints in reverse video, and `y` copies it again. The clipboard write goes
through a seam (Options.Clipboard, the OS clipboard by default) from a command, so the update
loop never waits on a broken clipboard; when the OS write fails the view falls back to OSC 52
and says so in the toast.

- No new dependency: `github.com/atotto/clipboard` is already in the module graph and writes
  the Windows clipboard directly through user32 (no process spawn, no console flash).
- Mouse reporting is already on in cell-motion mode (1002) for every non-modal screen, so
  press/motion/release arrive as `MouseClickMsg`/`MouseMotionMsg`/`MouseReleaseMsg`; nothing in
  the terminal setup changes.
- The selection is anchored to absolute stream line numbers and display columns, not screen
  coordinates, so it stays on its text while the job keeps writing and the window moves. A
  scroll keeps the selection; it may scroll out of view and back, unchanged.

## 2. Geometry

`renderPty` returns the pane head line, the optional notes (trimmed head, missed output, read
error), then `ptyRows()` content rows. `render()` joins the header, an optional banner, the gauge
and one blank row above that. The same helpers produce the click origin:

- content top = height(header) + height(banner) + height(gauge) + 1 blank + 1 pane head + notes.
- content rows = `ptyRows()`; row `i` of the window is absolute line `windowTop(rows) + i`.
- A click outside `[top, top+rows)` is not a selection event; it is ignored rather than clamped,
  so selecting near the help line cannot anchor into the help text.

Column 0 is the pane's first cell; `ptyClip` clips both display and copy to the same width, so a
column means the same thing to the highlight and to the extraction.

## 3. Model

```go
type ptyCell struct{ line int64; col int }
type ptySelection struct{ anchor, cursor ptyCell }
```

`ptyView` holds `sel *ptySelection` and `dragging bool`.

- press in the pane: `sel = &{anchor: cell, cursor: cell}`, `dragging = true`.
- motion with the button held: `cursor = cell` (only while dragging; motion outside a drag is
  ignored, and the model without all-motion reporting never sees idle motion anyway).
- release: `dragging = false`. If the anchor and cursor are the same cell the selection is
  cleared and nothing is copied (a plain click is not a copy). Otherwise the text is extracted,
  a copy command is returned, and the highlight stays until the next click or an esc.
- click without a preceding drag again clears.
- `y` re-copies the current selection.
- `esc` clears a selection first and only then leaves the screen, so the first esc is never
  "back" when it could have been "deselect".

The cursor and anchor are normalized before use, so dragging up or left works the same as down
or right.

## 4. Extraction

For each absolute line in `[first, last]`:

1. `raw := lines[line - baseAbs()]`; lines trimmed out of the bounded tail are read as blank
   (the selection can only span the visible window, so this is short).
2. `clipped := ptyClip(raw, w)` - the same clipping the display uses.
3. `left/right` are the anchor column for the first line, the cursor column plus one for the
   last, and the full width in between; `right` is clamped to the clamped line's width.
4. `ansi.Cut(clipped, left, right)` then `ansi.Strip`, then trailing spaces removed per line.
5. Trailing blank lines are dropped; interior blank lines are kept. Lines join with `\n`.

`ansi.Cut` is grapheme- and width-aware, so a trail that ends inside a wide character copies
the characters that fit, never half of one.

## 5. Painting

For each content row the absolute line decides whether it is selected; `selSpan` returns the
`[left, right)` cell range clamped to the line width. The row is walked once with the ANSI
decoder and reverse-video markers are inserted at grapheme starts: `selOn` before the first
selected cell, `selOff` after the last. The stream's own sequences pass through untouched, so
the colors after the selection stay exactly as the job wrote them, and a wide character is
painted whole because a marker never lands inside a grapheme. A reset inside the range would
turn reverse off, so every SGR decoded while inside re-arms `selOn`; the highlight survives the
stream's own repaint, which a pre-wrapped `Style.Render` of a stripped middle could not.

## 6. Clipboard

```go
Clipboard func(string) error // Options; default: clipboard.WriteAll
```

`y`/release return a command that writes the text off the update path and reports back:

- success: toast `copied N line(s), M chars`.
- failure: toast the reason and return `tea.SetClipboard(text)` (OSC 52, no-op on terminals
  without it). Nothing on the screen is lost either way.

## 7. Edge cases

- stream ends or head trimmed while a selection exists: selection stays; extraction reads what
  is still in the buffer.
- a selection spanning the whole window copies at most the window height of lines.
- copy with no selection is a no-op and does not toast.
- `--no-color`: reverse video is still emitted (it is an attribute, not a color), consistent
  with the rest of the muted palette.

## 8. Tests

- drag across two rows copies `"two\nthree"`-shaped text, boundary columns included, and the
  seam receives it.
- click without drag copies nothing and clears a previous selection.
- `y` re-copies; `esc` clears first, then leaves.
- the highlight appears in the rendered row for a selected cell and not outside it.
- scrolling keeps the selection anchored to its absolute lines.
- extraction strips ANSI and clips to the pane width.

## 9. Non-goals

- keyboard-arrow selection (Shift+arrow is not reliably distinguishable in cell-motion keys).
- a scrollback-wide selection: the view is bounded, the window is what is on screen.
- copying the job's raw bytes: the pane copies what it shows, clipped and stripped.
