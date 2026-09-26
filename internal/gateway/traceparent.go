package gateway

import (
	"encoding/json"
	"strings"
)

// A caller doing distributed tracing propagates its trace context in the W3C
// Trace Context traceparent request header
// (https://www.w3.org/TR/trace-context/). OpenTelemetry SDKs and every other
// W3C-compatible tracer send it by default. The header carries the trace-id
// the caller indexed this request under in its own trace storage, and this
// file promotes that id from the captured header snapshot into a column the
// audit row can later be joined on.
//
// The parser is strict and minimal on purpose. Only version 00 — the version
// whose semantics this parser knows — is understood; everything else (missing
// header, empty value, future versions, uppercase hex, wrong widths, all-zero
// ids, stray whitespace) yields the empty string, which the audit row stores
// as NULL. Nothing is ever partially extracted and no id is ever invented: a
// non-NULL value in the log always means the caller itself was tracing. The
// tracestate header is not parsed.

// traceparentKey is the key the masked header snapshot stores traceparent
// under. The snapshot is produced by SanitizeHeaders from an http.Header,
// whose keys are canonical by the time the gateway sees them ("Traceparent"),
// so an exact lookup is the whole matching rule — a case-insensitive scan is
// deliberately absent, because anything the HTTP layer did not canonicalize
// into that exact key is not a traceparent this gateway captured.
const traceparentKey = "Traceparent"

// traceIDFromHeaderSnapshot returns the caller's W3C trace-id as captured in
// the masked request-header snapshot, or "" when the snapshot carries no
// usable traceparent. Reading the snapshot rather than the live request
// header is what keeps the audit row column and the stored header JSON in
// agreement: the value written beside the capture is always derivable from
// the capture.
func traceIDFromHeaderSnapshot(snapshot []byte) string {
	if len(snapshot) == 0 {
		return ""
	}
	var headers map[string][]string
	if err := json.Unmarshal(snapshot, &headers); err != nil {
		return ""
	}
	values := headers[traceparentKey]
	if len(values) != 1 {
		// Absent, or repeated. Two traceparent values cannot be reconciled
		// into one trace, so a repeated header is refused rather than one of
		// its values silently preferred.
		return ""
	}
	return parseTraceparent(values[0])
}

// parseTraceparent validates one traceparent value against the version-1
// grammar and returns its trace-id, or "" when any part of the value fails.
// All four segments must be lowercase hex (HEXDIGLC in the grammar: digits
// and a-f only), and the two id segments must additionally be non-zero.
func parseTraceparent(v string) string {
	parts := strings.Split(v, "-")
	if len(parts) != 4 {
		return ""
	}
	version, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]
	// Version 00 only. A higher version is a future format whose fields may
	// mean something else; guessing at it could persist an id that is not
	// the caller's trace-id at all.
	if version != "00" {
		return ""
	}
	if !lowerHex(traceID, 32) || !lowerHex(parentID, 16) || !lowerHex(flags, 2) {
		return ""
	}
	// An all-zero id is the format's own "unset" marker; persisting it as if
	// the caller had been tracing would write a value no trace system keys
	// anything under.
	if allZeros(traceID) || allZeros(parentID) {
		return ""
	}
	return traceID
}

// lowerHex reports whether s is exactly n lowercase hexadecimal digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// allZeros reports whether s consists solely of the digit 0.
func allZeros(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
