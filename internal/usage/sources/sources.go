// Package sources holds one file per provider that can report its
// subscription quota (T6.3, spec wiki/port/provider-usage.md §2.2). Each file
// registers itself in init(), so adding a provider never edits the core
// package or a sibling source — the fork-friendly shape the spec asks for.
//
// Provenance discipline (spec §2.2 and grok's usage research README): every
// source file names where its endpoint, credential and semantics came from and
// whether they were verified against the live service. Recorded responses live
// under testdata/, desensitized, so the parsers are table-tested with no
// network.
package sources

import "time"

// nowFunc stamps the reset-legitimacy check (rule 3). Tests pin it to the
// recorded fixture's anchor time, so a body captured on one day keeps the same
// reading forever instead of going stale as its reset times age.
var nowFunc = time.Now
