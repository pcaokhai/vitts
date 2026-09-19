// Package cache derives cache keys and stores synthesized audio (ADR-006).
//
// The key is tenant-agnostic: identical input produces identical audio, and the audio
// carries no tenant data, so a hit crosses tenants by design.
package cache

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// Params are the synthesis knobs that change the audio. Anything not here must not
// change the output, or the cache would return the wrong thing.
type Params struct {
	CFGScale               float64
	AudioTemperature       float64
	AudioTopK              int32
	AudioTopP              float64
	AudioRepetitionPenalty float64
	EOAExtraFrames         int32
	SampleRate             int32
}

// Input is everything the key is derived from.
type Input struct {
	ModelVersion string
	VoiceID      string
	Normalize    bool
	Params       Params
	// Text is the text as the worker will receive it. The caller normalises first when
	// Normalize is set, so the key matches the audio that is actually produced.
	Text string
}

// Key is the sha256 the schema stores in audio_cache.cache_key.
type Key [sha256.Size]byte

// String is the hex form used in Redis keys and S3 paths.
func (k Key) String() string { return fmt.Sprintf("%x", k[:]) }

// Bytes is the form the database column takes.
func (k Key) Bytes() []byte { return k[:] }

// Derive computes the cache key.
//
// Every field is canonicalised before hashing so that inputs which produce identical
// audio produce an identical key: "0.8" and "0.80" are the same temperature, and runs of
// whitespace in the text are the same text (US-11 acceptance criterion 2). The model
// version is part of the key, so an upgrade invalidates the cache rather than serving
// audio from weights that no longer exist (ADR-006, US-11 acceptance criterion 3).
func Derive(in Input) Key {
	var b strings.Builder
	b.WriteString(in.ModelVersion)
	b.WriteByte('|')
	b.WriteString(in.VoiceID)
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(in.Normalize))
	b.WriteByte('|')
	b.WriteString(canonicalParams(in.Params))
	b.WriteByte('|')
	b.WriteString(CanonicalText(in.Text))

	return sha256.Sum256([]byte(b.String()))
}

// CanonicalText collapses whitespace and trims the ends.
//
// It deliberately does not change case or punctuation: those change how the model speaks,
// so they must change the key.
func CanonicalText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// canonicalParams renders the knobs in a fixed order with a fixed precision, so equal
// values always render equal regardless of how the caller spelled them.
func canonicalParams(p Params) string {
	return strings.Join([]string{
		"cfg=" + canonicalFloat(p.CFGScale),
		"temp=" + canonicalFloat(p.AudioTemperature),
		"topk=" + strconv.Itoa(int(p.AudioTopK)),
		"topp=" + canonicalFloat(p.AudioTopP),
		"rep=" + canonicalFloat(p.AudioRepetitionPenalty),
		"eoa=" + strconv.Itoa(int(p.EOAExtraFrames)),
		"rate=" + strconv.Itoa(int(p.SampleRate)),
	}, ",")
}

// canonicalFloat renders to 4 decimals, which is finer than any knob's meaningful
// resolution and coarse enough that float noise cannot split one value into two keys.
func canonicalFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}
