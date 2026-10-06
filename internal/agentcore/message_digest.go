package agentcore

import (
	"crypto/sha256"
	"encoding/hex"
)

// MessageTextDigest returns a short deterministic digest of the text a message
// presents: role discriminant + rendered text content, hashed with SHA-256 and
// hex-encoded (first 32 chars). It is the content anchor of a context edit
// (T3.4): stable across session persist/load round-trips (rendered text only,
// never full JSON — Details maps would not round-trip byte-stable), so the
// projection can re-locate an edited target after markers or compaction
// shifted the list.
func MessageTextDigest(m Message) string {
	h := sha256.New()
	h.Write([]byte(m.Role()))
	h.Write([]byte{0})
	h.Write([]byte(ContentToText(contentOf(m))))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// contentOf extracts the content list of the three LLM-bound message roles
// (the only legal context-edit targets); marker roles have none.
func contentOf(m Message) ContentList {
	switch msg := m.(type) {
	case UserMessage:
		return msg.Content
	case AssistantMessage:
		return msg.Content
	case ToolResultMessage:
		return msg.Content
	default:
		return nil
	}
}
