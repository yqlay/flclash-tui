# Linux TUI translations

The JSON catalogs here are source resources embedded into the binary. There is no runtime download, translation service, or locale-dependent backend state. CLI output and Flutter localization are separate.

- `en.json` is the fallback catalog. Other catalogs contain initial translation drafts; native-speaker review is still needed, especially for longer instructions.
- Existing `ui.*` identifiers are stable legacy source IDs. Add descriptive keys for new messages, and keep formatting arguments in the same order and with the same verbs in all catalogs.
- User data (names, URLs, paths, addresses, raw diagnostics) is passed as opaque arguments, never looked up as a translation key.
- `metadata.json` describes notification level, kind, and progress independently of translated text. Do not infer error/progress state by searching UI strings.
- `Count` selects CLDR cardinal forms using the existing `golang.org/x/text` dependency. Missing forms use the locale's `other` form, then English.
- Store new text as a message descriptor until render time so asynchronous results and existing notifications follow the current frontend language. Canonical English remains available for redacted logging.
- Field resources contain labels and value formats, not alignment spaces. TUI layout measures terminal cells, chooses a locale-aware sidebar width, and shares viewport geometry with input and notification scrolling. Long values wrap; optional list columns yield space first.
- Grapheme boundaries are for editing/clipping, not a substitute for terminal cell widths. Spacing marks such as Bengali vowel signs still consume a cell even when their grapheme property is `Extend`. Measurements, padding, wrapping, and clipping share `tuiDisplayWidth`; preserve native names in the language picker and omit English descriptions first when space is limited.

Run from `core/`:

```sh
CGO_ENABLED=0 GOOS=linux go test -tags cli ./...
CGO_ENABLED=0 GOOS=linux go vet -tags cli ./...
```

Tests validate catalog completeness/format arguments, opaque data, persistence, concurrent saves, all language/page/overlay layouts, visible navigation labels, cell-aligned values, overlay controls, resizing with an active input cursor, and measured chart height. These checks cannot certify font shaping or linguistic accuracy.

For independent terminal-cell and incremental-rendering regression tests, install the development-only xterm dependencies from the repository root, then run:

```sh
npm ci --prefix packaging/terminal-tests
cd core
FLCLASH_TUI_REQUIRE_TERMINAL_TESTS=1 CGO_ENABLED=0 GOOS=linux go test -tags cli -run TestTUILanguageTerminalEmulator -v .
```

The test checks every native-language option in every UI locale at four viewport sizes, then feeds actual Bubble Tea output through an xterm Unicode 11 cell buffer while opening, scrolling, resizing, cancelling, and saving. Node/xterm are not runtime or installation dependencies. Without these test dependencies, normal Go tests skip only this integration test; CI requires it. `FLCLASH_TUI_XTERM_MODULES` can point to an existing `node_modules` directory containing both xterm packages for local diagnostics. Older terminal width tables and font shaping can still differ from this test baseline.
