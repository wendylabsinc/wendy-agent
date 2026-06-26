# `wendy os install` device picker — section headers

**Date:** 2026-06-25
**Status:** Approved

## Goal

During `wendy os install`, group the interactive device list under
non-selectable section headers — **WendyOS** first, then **Wendy Lite** — and
have arrow-key navigation skip over the header rows. Rename the WendyOS group's
displayed label from "Linux" to "WendyOS".

WendyOS is the primary, more important target and therefore comes first.

## Background

The picker is a shared Bubble Tea component (`go/internal/cli/tui/picker.go`)
backed by a single flat, keyboard-navigable `bubbleTable`. It has no native
concept of section headers, and many existing call sites (device discovery,
WiFi, project/model pickers) rely on the table cursor index mapping 1:1 to the
visible item index. The `os install` picker is one-shot: it sends one
`PickerAddMsg` followed by `PickerDoneMsg` (see `pickFromItems` in
`go/internal/cli/commands/project.go`), so there is no live re-sorting to worry
about.

Today `runOSInstall` (`go/internal/cli/commands/os_install.go`) builds picker
items for Linux devices (category `"Linux"`) and, when `--device-type` is not
set, two ESP32 entries (category `"Wendy Lite"`). The category is shown as a
suffix in the Description column. There are no headers and no enforced ordering
between the two groups.

## Design

### Invariant: zero behavior change without sections

A new `Section string` field is added to `PickerItem`. When **no** visible item
sets `Section`, the picker renders and navigates exactly as before — same rows,
same cursor↔item indexing, same output. Section behavior engages only when at
least one visible item has a non-empty `Section`. This keeps every other picker
(and all existing picker tests) untouched.

### 1. `PickerItem.Section` (`tui/picker.go`)

Add `Section string` to `PickerItem`. Items are grouped under a header bearing
the section name. Section order follows the order each section first appears in
the already-sorted item list, so callers control grouping order via `SortKey`.

### 2. Interleaved header rows + cursor↔item mapping

Add `rowItem []int` to `PickerModel`: one entry per table row, holding the index
into the visible-items slice, or `-1` when the row is a section header.

In `refreshTableWithCursorKey`, after the existing `SortKey` sort (which keeps
items of the same section contiguous and ordered):

- If any visible item has a `Section`, walk the visible items in order. When an
  item's section differs from the previous emitted section, splice a header row
  in front of it. Build `rowItem` in parallel (header → `-1`, item → its visible
  index).
- Otherwise, build rows exactly as today and set `rowItem` to the identity
  mapping.

The item rows themselves are still produced by the existing
`pickerTableDataForColumns` path; header rows are spliced into the resulting row
slice at the right positions so column widths still account for them.

### 3. Navigation skips headers

The cursor must never rest on a header row.

- In the `default` key branch, record the cursor before forwarding the key to
  the table, then after the table updates check whether the new cursor row is a
  header (`rowItem[cursor] < 0`). If so, step one row in the direction of travel
  (down if the cursor moved down or stayed, up otherwise) to the nearest
  selectable row; if the edge is reached, reverse to find the first selectable
  row.
- Initial cursor and cursor-restore (`refreshTableWithCursorKey`) snap to the
  first selectable row, never a header.
- `enter`, `d`, `x`, the selected-item hint, and the insecure-mTLS warning all
  resolve the highlighted item through `rowItem` instead of assuming
  `cursor == visible index`. `enter`/`d`/`x` are no-ops if the cursor is somehow
  on a header.

### 4. Header styling

The section label renders in the Name column as a bold `── WendyOS`-style cell
(lipgloss styling embedded in the cell string; remaining columns blank). Because
the cursor never lands on a header, it is never drawn with the selected-row
highlight.

### 5. `os_install.go`

- WendyOS (Linux) devices: `Section: "WendyOS"`, `SortKey: "0_wendyos_<name>"`,
  and the category label changes from `"Linux"` to `"WendyOS"`.
- ESP32 devices: `Section: "Wendy Lite"`, `SortKey: "1_lite_<name>"`.
- The Description column drops the redundant category suffix (the header now
  states the group); it shows just the version.

### 6. Edge cases

- **One group only** (e.g. the Linux manifest fetch fails and only the two
  ESP32 entries remain): the single section's header still renders. This is
  simpler than conditionally suppressing it and remains accurate.
- **`--device-type` set**: the interactive picker is never shown, so this path
  is unaffected.
- **Empty section**: a header is only emitted when an item introduces the
  section, so a group with no devices produces no header.

## Testing

New unit tests in `tui/picker_test.go`:

- Section headers render with their labels.
- `KeyDown` from the last item of a section skips the following header and lands
  on the next section's first item.
- `enter` selects the correct item when a header precedes it.
- A picker with no `Section` set behaves identically (cursor index == item
  index), guarding the invariant above.

All existing picker tests must remain green.
