package synth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/synth"
)

func valid() synth.Request {
	return synth.Request{Text: "Xin chào.", VoiceID: "maichi"}
}

func TestValidateFillsContractDefaults(t *testing.T) {
	t.Parallel()

	req := valid()

	require.NoError(t, req.Validate())
	require.Equal(t, synth.FormatWAV, req.Format)
	require.Equal(t, int32(48000), req.SampleRate)
}

func TestValidateRejectsEmptyText(t *testing.T) {
	t.Parallel()

	req := valid()
	req.Text = ""

	require.ErrorIs(t, req.Validate(), synth.ErrInvalidRequest)
}

// US-08 acceptance criterion 3: the limit is characters, not bytes.
func TestTextLimitCountsCharactersNotBytes(t *testing.T) {
	t.Parallel()

	req := valid()
	req.Text = string(make([]rune, synth.MaxTextChars))
	for i := range req.Text {
		_ = i
	}
	req.Text = ""
	for range synth.MaxTextChars {
		req.Text += "ố" // 3 bytes each: 9000 bytes, exactly 3000 characters
	}

	require.NoError(t, req.Validate(), "3000 Vietnamese characters must fit")
	require.Equal(t, int64(synth.MaxTextChars), req.Chars())

	req.Text += "a"
	require.ErrorIs(t, req.Validate(), synth.ErrTextTooLong)
}

func TestValidateRejectsUnknownFormatAndSampleRate(t *testing.T) {
	t.Parallel()

	format := valid()
	format.Format = "flac"
	require.ErrorIs(t, format.Validate(), synth.ErrInvalidRequest)

	rate := valid()
	rate.SampleRate = 44100
	require.ErrorIs(t, rate.Validate(), synth.ErrInvalidRequest)
}

// mp3 and ogg need the worker's Merge RPC, which lands in task 2.2.
func TestUnavailableFormatsAreRefusedPlainly(t *testing.T) {
	t.Parallel()

	for _, format := range []synth.Format{synth.FormatMP3, synth.FormatOggOpus} {
		req := valid()
		req.Format = format

		err := req.Validate()

		require.ErrorIs(t, err, synth.ErrFormatNotReady)
		require.Contains(t, err.Error(), "2.2", "the refusal names the task that delivers it")
	}
}

func TestValidateRequiresAVoice(t *testing.T) {
	t.Parallel()

	req := valid()
	req.VoiceID = ""

	require.ErrorIs(t, req.Validate(), synth.ErrInvalidRequest)
}
