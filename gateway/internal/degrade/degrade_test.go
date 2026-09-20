package degrade_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/degrade"
)

// T-106. The window bounds a continuous outage, and a success resets it.
func TestBreaker(t *testing.T) {
	now := time.Unix(0, 0)
	b := degrade.New(60*time.Second, func() time.Time { return now })

	require.False(t, b.Degraded())
	require.True(t, b.Tolerate(), "the first failure is inside the window")
	require.True(t, b.Degraded())

	now = now.Add(59 * time.Second)
	require.True(t, b.Tolerate())

	now = now.Add(2 * time.Second)
	require.False(t, b.Tolerate(), "past the window the caller must stop serving")

	b.Succeed()
	require.False(t, b.Degraded())
	require.True(t, b.Tolerate(), "a recovered dependency starts a fresh window")
}

// A brief blip every minute must not accumulate into a refusal: each success clears it.
func TestRepeatedBlipsDoNotAccumulate(t *testing.T) {
	now := time.Unix(0, 0)
	b := degrade.New(60*time.Second, func() time.Time { return now })

	for range 10 {
		require.True(t, b.Tolerate())
		b.Succeed()
		now = now.Add(30 * time.Second)
	}
}
