// Package lsp implements the LSP client kernel (T8.2): a stdio JSON-RPC
// client for language servers (LSP 3.17), today adapted for gopls.
//
// The core workflow is the overlay: pigo's edits are pushed to the server
// with textDocument/didOpen and didChange as in-memory state, so diagnostics
// follow the edited content the moment a tool asks for them instead of
// waiting for the server's file watcher to notice the write. publishDiagnostics
// are collected per URI; the lsp_* tool family reads them back.
//
// Process lifecycle is owned by the Manager: lazy start on first use (plus an
// optional startup prewarm), idle reclaim after a configurable quiet period,
// and clean shutdown on run end. gopls-specific behavior (default command and
// arguments, -remote=auto daemon reuse, start-failure degradation) lives in
// gopls.go; the rest of the package is server-agnostic.
//
// The package is a leaf: no internal dependencies, so both the agenttool
// layer and the config surface can hold a *Manager without import cycles.
package lsp
