package model

// Envelope is the on-disk representation of a value-slot (process input, a task
// output, the process output, an external payload). A slot is ALWAYS stored as an
// envelope, never as a raw value, so user data is always nested under Data and is
// never confused with the envelope itself — there is no in-band sentinel to collide
// with arbitrary user JSON.
//
// Exactly one of Data / Refs is populated:
//   - Data: the value is small enough to keep inline.
//   - Refs: the value lives in process_objects; v1 holds a single root reference
//     (no Path), i.e. the whole slot is externalized.
//
// The shape is intentionally forward-compatible: ObjectRef.Path (granular/nested
// externalization) and an encryption discriminator are additive later without
// changing how existing envelopes decode.
type Envelope struct {
	Data any          `json:"data,omitempty"`
	Refs []*ObjectRef `json:"refs,omitempty"`
	// Preview is a short, human-readable excerpt of an externalized value, set only
	// for log payloads so a log listing can show a snippet without loading the object.
	Preview string `json:"preview,omitempty"`
}

// ObjectRef points at one row in process_objects. Ref is the sha256 hex of the
// stored content; it doubles as the object id and the change-detection key (a
// re-encoded value with the same hash needs no new write). Size is the byte length
// of the content, surfaced to the API without loading the object.
type ObjectRef struct {
	Ref  string `json:"ref"`
	Size int64  `json:"size"`
}

// IsRef reports whether the envelope externalizes its value (vs. holding it inline).
func (e Envelope) IsRef() bool { return len(e.Refs) > 0 }
