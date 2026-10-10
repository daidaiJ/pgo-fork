# usage source fixtures

Recorded/derived quota responses, one directory per provider. `Parse` is a
pure function of these bodies, so the parsers are tested with no network
(spec `wiki/port/provider-usage.md` §2.2).

Provenance (see grok-build-proxy
`docs-local/usage/quota-endpoints-and-credentials.md` for the survey itself):

| File | Provenance |
| --- | --- |
| `commandcode/goat.json` | Live Command Code responses (real key, 2026-10-10): field names, types and units are verbatim; ids, price id and account metadata replaced with fixture values. `windowLimits.limited: true` with every `exceeded: false` is what the service really returns. |
| `commandcode/aliases.json` | Synthetic: the defensive key spellings (`five_hour`/`rolling5h`, `cap`/`limit`, `resetAt`/`resetsAt`) plus numeric-string values and a failed subscriptions call. |
| `opencode-go/go.json` | Documented shape only — there is no OpenCode Go key on this machine. `percent` is the used share and `resetsAt` an ISO timestamp, per the upstream route read in the survey. |
| `opencode-go/reset-beyond-window.json` | Synthetic: a reset time far outside its window, to pin the rule-3 drop. |
| `opencode-go/empty.json` | Synthetic: a body with no recognized windows, to pin the rule-1 "unknown, never 0%" posture. |
