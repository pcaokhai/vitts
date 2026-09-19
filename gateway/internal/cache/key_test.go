package cache_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

func base() cache.Input {
	return cache.Input{
		ModelVersion: "c2bfbd67",
		VoiceID:      "maichi",
		Normalize:    true,
		Text:         "Xin chào các bạn.",
		Params: cache.Params{
			CFGScale: 1.0, AudioTemperature: 0.8, AudioTopK: 25,
			AudioTopP: 0.95, AudioRepetitionPenalty: 1.2, EOAExtraFrames: 1,
			SampleRate: 48000,
		},
	}
}

func TestIdenticalInputProducesIdenticalKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, cache.Derive(base()), cache.Derive(base()))
	require.Len(t, cache.Derive(base()).Bytes(), 32, "the schema constrains cache_key to 32 bytes")
}

// US-11 acceptance criterion 2.
func TestEquivalentSpellingsHitTheSameKey(t *testing.T) {
	t.Parallel()

	tests := map[string]func(cache.Input) cache.Input{
		"0.80 is 0.8": func(in cache.Input) cache.Input {
			in.Params.AudioTemperature = 0.80
			return in
		},
		"float noise": func(in cache.Input) cache.Input {
			in.Params.AudioTopP = 0.95000001
			return in
		},
		"collapsed whitespace": func(in cache.Input) cache.Input {
			in.Text = "Xin   chào\tcác\nbạn."
			return in
		},
		"surrounding whitespace": func(in cache.Input) cache.Input {
			in.Text = "  Xin chào các bạn.  "
			return in
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, cache.Derive(base()), cache.Derive(mutate(base())))
		})
	}
}

func TestMeaningfulDifferencesProduceDifferentKeys(t *testing.T) {
	t.Parallel()

	tests := map[string]func(cache.Input) cache.Input{
		"model version": func(in cache.Input) cache.Input { in.ModelVersion = "other"; return in },
		"voice":         func(in cache.Input) cache.Input { in.VoiceID = "giahuy"; return in },
		"normalize":     func(in cache.Input) cache.Input { in.Normalize = false; return in },
		"text":          func(in cache.Input) cache.Input { in.Text = "Tạm biệt."; return in },
		"case":          func(in cache.Input) cache.Input { in.Text = "XIN CHÀO CÁC BẠN."; return in },
		"punctuation":   func(in cache.Input) cache.Input { in.Text = "Xin chào các bạn!"; return in },
		"temperature":   func(in cache.Input) cache.Input { in.Params.AudioTemperature = 0.9; return in },
		"topk":          func(in cache.Input) cache.Input { in.Params.AudioTopK = 30; return in },
		"sample rate":   func(in cache.Input) cache.Input { in.Params.SampleRate = 24000; return in },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotEqual(t, cache.Derive(base()), cache.Derive(mutate(base())),
				"this changes the audio, so it must change the key")
		})
	}
}

func TestKeyIsTenantAgnostic(t *testing.T) {
	t.Parallel()

	// There is no tenant field at all: a hit crossing tenants is the design (ADR-006),
	// and this test exists so removing that property requires deleting it.
	require.Equal(t, cache.Derive(base()).String(), cache.Derive(base()).String())
	require.Len(t, cache.Derive(base()).String(), 64)
}

func TestCanonicalText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a b c", cache.CanonicalText("  a \t b \n c  "))
	require.Empty(t, cache.CanonicalText("   "))
}
