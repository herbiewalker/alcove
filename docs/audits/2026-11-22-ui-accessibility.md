# Audit pass: UI and accessibility

- **Date:** 2026-11-22
- **Lens:** UI / accessibility (contrast, focus order, labels, adaptive layout)
- **Tree:** `main` at `92ad3f7` (CI green)
- **Verified by:** reading every template, `static/app.css` and `static/ui.js`; scripted checks over all 47 templates (control-to-label pairing, `<img>` alt, one `<h1>` per page, table headers, inline handlers); WCAG contrast ratios computed from the CSS colour tokens (both themes). **Not done:** no screen reader, no real phone, no browser run, so focus behaviour and reflow at 320 px / 200 % zoom are read from the CSS, not observed.

## Scope read
`templates/*.html` (47), `templates/base.html`, `static/app.css`, `static/ui.js`.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-----|-----|
| 1 | should-fix | `app.css` (inputs, `textarea`, `select`, `.card`, `.scroll`), tokens `--line` | The only visible edge of a text field is a 1 px `--line` border: 1.27:1 against the page in light, 1.42:1 in dark. WCAG 1.4.11 needs 3:1 for a control's boundary. Fields are hard to find for low-vision users. | Give form controls their own border token of at least 3:1 (`--field-line`) in both themes. Cards can keep `--line`. |
| 2 | should-fix | `templates/users.html:43-81`, `myentries.html:13`, `uploads.html:34`, `mydeleted.html:12`, and the other row tables (19 generic buttons) | Row action buttons are named only "Delete", "Restore", "Disable", "Reset password", "Cancel", "Private: share it". With a screen reader the buttons list is N identical names, and for a destructive action the person cannot tell which row it acts on. | Add `aria-label` with the row's name or date (`Disable {{.Name}}`, `Delete upload of {{.When}}`). |
| 3 | should-fix | `static/ui.js:35-40` (`clock`), `templates/uploads.html:23` (`#status`) | The busy banner (`role=status`, `aria-live=polite`) and the upload status are rewritten every second and on every percent, so a screen reader announces "Working... (4 s)", "(5 s)" and so on until the job ends. It drowns out everything else on the page. | Announce the message once (set the live region only when the base text changes); mark the ticking clock and percentage `aria-hidden`, or throttle announcements to every 10 s and on completion. |
| 4 | should-fix | `templates/*` (all), `base.html:14-27` | No "skip to content" link. Nine nav items and two forms come before `<main>` on every page, a bypass-blocks failure (WCAG 2.4.1) for keyboard users. | Add a skip link as the first element in `<body>` and `id="main"` on `<main>`; style it visible on focus. |
| 5 | should-fix | `static/ui.js:166-192` (hide screen) | The cover is a button appended to `<body>`, but the page behind stays in the tab order and the accessibility tree. Tab from the cover walks through invisible links, and a screen reader keeps reading the hidden content aloud, which defeats the feature for the person it is for. `uncover` also removes an `aria-hidden` that is never set. | Set `inert` on the header, `main` and footer while covered, remove it on uncover; drop the dead `aria-hidden` line. |
| 6 | should-fix | `.scroll` wrappers (18 tables) | A horizontally scrolling region that holds no focusable item cannot be scrolled with the keyboard in browsers that do not make scrollers focusable (Safari, older Chrome). Wide tables on a phone with a keyboard or switch control are unreachable. | `tabindex="0"`, `role="region"` and an `aria-label` on each `.scroll` (or a small `tableBox` template). |
| 7 | nit | `app.css` `.cal td.out a.day { opacity: .45 }` | Days outside the month are still links but drop to 2.73:1 (light). 3.97:1 in dark. | Use a muted colour that holds 4.5:1 instead of opacity. |
| 8 | nit | 26 `<table>`s without a `<caption>` or `aria-label` | Tables have `<th>` but no name, so a screen reader says only "table" and the row count. 14 header cells are empty (`<th></th>`, the action columns). | Add a visually hidden `<caption class="sr">`, and `<th><span class="sr">Actions</span></th>` for empty headers. |
| 9 | nit | `app.css` `td a { color: inherit; font-weight: 600 }` | Links in table cells differ from the text around them by weight only, no underline or colour (WCAG 1.4.1). | Keep the underline in tables. |
| 10 | nit | `base.html:8`, `backup.go:133` | While a backup job runs the page reloads itself every 4 s with `<meta http-equiv=refresh>`. It resets scroll and focus, and gives no way to stop it (WCAG 2.2.1). | Acceptable for a short job; better to refresh with the script and a visible "Pause refresh" or at least a longer interval. |
| 11 | nit | `app.css` `.mk` | Calendar initials are 10 px (`.65rem`). They are decorative (`aria-hidden`; the day's `aria-label` carries the data), but sighted low-vision users rely on them. | `.75rem`, and enough cell height to hold two rows. |

No blockers.

## What held up
- **Contrast of text:** every text pair passes AA in both themes (lowest: success text 4.69:1 light; muted text 5.9:1; buttons 6.7:1 and up; dark theme 8:1 and up); calendar marks 9.5:1 and up; focus ring 5.15:1 and up.
- **Labels:** every form control with an `id` has a matching `<label for>`; every control without an `id` is wrapped in a `<label>`. No unlabelled input anywhere.
- **Structure:** `lang="en"`, a viewport that allows zoom (no `user-scalable=no`), one `<h1>` on all 46 content pages, `aria-current` on the nav and the calendar, `<nav>` landmarks with names, `role=status` / `role=alert` on notices, `aria-pressed` on the theme buttons.
- **Images and media:** every `<img>` has an `alt` (decorative ones are empty); photos are blurred, reachable by keyboard, and unblur on Enter as well as tap.
- **Forms and focus:** 16 px inputs (no iOS zoom), 44 px touch targets, a 3 px `:focus-visible` ring everywhere, `autofocus` only on the first field of single-field sign-in screens.
- **Motion and theme:** `prefers-reduced-motion` honoured; light, dark and auto themes with `color-scheme`; the page works with the script off (forms submit, jobs finish).
- **Layout:** mobile-first single column, safe-area insets, tables scroll in their own box and never widen the page, no inline styles or handlers.
- **Calendar:** a real table with a caption and column headers; each day link's name carries the date and per-member counts, so the colours are never the only carrier.

## Verdict
- **Covered:** markup, CSS and script for every page, plus computed contrast in both themes.
- **Cannot cover without more tooling:** real screen-reader behaviour (NVDA, VoiceOver, TalkBack), reflow at 320 px and 400 % zoom, Windows high-contrast mode, touch behaviour on a phone. A short manual pass on a real phone and with one screen reader is the remaining gate for this lens.
- **Blockers:** none. Six should-fix items (1 to 6) are cheap and worth doing before the next release.

## Resolution (same day)
- **1 fixed:** new `--field-line` token (light `#8a7a96`, 3.7:1 to 4.0:1; dark `#8f7aa6`, 4.6:1 to 5.0:1) on text fields, textareas and selects.
- **2 fixed:** all 19 generic row buttons carry an `aria-label` that names the person, file, space, invite or entry (the visible word stays at the start of the label).
- **3 fixed:** the busy banner shows its clock `aria-hidden` and announces its message once from a hidden twin; the upload status keeps its visual updates but only starting, checking and failure go to a separate live region.
- **4 fixed:** skip link as the first element in `<body>` (visible on focus), `<main id="main" tabindex="-1">`.
- **5 fixed:** Hide screen sets `inert` on the header, main, footer and banner while the cover is up and clears it on return; the dead `aria-hidden` line is gone.
- **6 fixed:** every `.scroll` box is `role="region"`, `tabindex="0"` and named, with a focus ring.
- **7 fixed:** out-of-month days use the muted colour (5.9:1) instead of opacity.
- **8 fixed:** all 26 tables have a (hidden) caption; the 14 empty header cells say "Actions".
- **9 fixed:** links in table cells are underlined.
- **10 fixed:** the meta refresh is now every 10 s; with the script on it is replaced by a timer and a "Pause automatic refresh" button. Without the script the meta refresh stays as the fallback.
- **11 fixed:** calendar initials are 12 px in 18 px marks, day cells 60 px high.
- **Guard:** `TestTemplatesKeepAccessibilityRules` checks the source for focusable named scroll boxes, a caption per table, no empty header cells, named row buttons, the skip link and the field-border token.
- **Still open:** the manual pass on a real phone and with a screen reader (not possible from this machine).
