# TUI UX Consistency Guide

Reference for agents modifying the TUI (`internal/tui/`). Follow these patterns for all new and modified interactions.

## Core Principles

1. **Lists use arrow navigation, not letter keys.** Any time the user picks from a set of options, use `up/down` (+ `j/k`) with `enter` to select and `esc` to go back.
2. **Confirmations use `enter/esc`, not `y/n`.** Prompt text should read "Press enter to confirm, esc to cancel" (or similar).
3. **`esc` always means cancel/back.** Never require a different key to go back. Every modal, wizard step, and confirmation must respond to `esc`.
4. **`enter` always means proceed/submit** in single-line contexts.
5. **`ctrl+d` submits multiline textareas** (enter inserts newlines). This is the one exception to rule 4.
6. **Vim keys (`j/k`) are supported wherever arrow keys navigate lists.** Do not add them to free-form text inputs.

## Interaction Patterns

### List/Menu Selection
Use for: picking from a set of options (repos, filter types, conflict resolution, settings fields).

```
Navigation:  up/k, down/j
Select:      enter
Cancel:      esc
```

All list items should have a visual cursor/highlight on the focused item.

### Confirmation Prompt
Use for: any yes/no decision before an action (autopilot launch/stop, analysis, cleanup).

```
Confirm:  enter
Cancel:   esc
Prompt:   "Do X? (enter to confirm, esc to cancel)"
```

### Single-Line Text Input
Use for: issue numbers, setting values, filter text input.

```
Submit:   enter
Cancel:   esc
```

### Multiline Textarea
Use for: broadcast messages, user messages, onboarding, analyzer focus.

```
Submit:   ctrl+d
Cancel:   esc
```

### Multi-Step Wizards
Use for: filter flow, track/untrack flow.

- Each step follows one of the patterns above.
- `esc` goes back one step (or exits on the first step).
- Maintain consistent navigation within the wizard — don't switch between arrow-nav and letter-key-nav between steps.

## Known Issues (To Fix)

| Location | Current | Should Be |
|----------|---------|-----------|
| `filter.go` filterStepSelectType | Letter keys `l/m/p/a` | Arrow-nav list |
| `filter.go` filterStepConflict | Letter keys `u/a/c` | Arrow-nav list |
| `app.go` pollConfirm | `y/n` | `enter/esc` |
| `app.go` autopilot scan-confirm | `y/n` | `enter/esc` |
| `app.go` autopilot confirm (launch) | `y/n` | `enter/esc` |
| `app.go` autopilot stop-confirm | `y/n` | `enter/esc` |
| `app.go` track cleanup confirm | `y/n` | `enter/esc` |

## Style Notes

- Spinner: `spinner.MiniDot` for all async operations.
- Help overlay (`?`): update when adding new keybindings.
- Bottom bar: show context-appropriate key hints for the current mode.
