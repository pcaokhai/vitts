package jobs_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pcaokhai/vitts/gateway/internal/jobs"
)

// T-10: the SSRF guard is what stops a webhook from becoming a way into our network.
func TestSSRFGuardRejectsInfrastructure(t *testing.T) {
	t.Parallel()

	guard := jobs.NewSSRFGuard()

	tests := map[string]string{
		"plain http":        "http://example.com/hook",
		"loopback literal":  "https://127.0.0.1/hook",
		"loopback name":     "https://localhost/hook",
		"ipv6 loopback":     "https://[::1]/hook",
		"private 10.x":      "https://10.0.0.5/hook",
		"private 192.168.x": "https://192.168.1.10/hook",
		"private 172.16.x":  "https://172.16.4.4/hook",
		"cloud metadata":    "https://169.254.169.254/latest/meta-data/",
		"unique local v6":   "https://[fd00::1]/hook",
		"carrier-grade nat": "https://100.64.1.1/hook",
		"unspecified":       "https://0.0.0.0/hook",
		"no host":           "https:///hook",
		"not a url":         "://nope",
		"ftp":               "ftp://example.com/hook",
	}

	for name, rawURL := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := guard.Check(context.Background(), rawURL)

			require.ErrorIs(t, err, jobs.ErrWebhookRejected, "%s must be refused", rawURL)
		})
	}
}

func TestSSRFGuardAllowsAPublicHTTPSURL(t *testing.T) {
	t.Parallel()

	guard := jobs.NewSSRFGuardWithResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})

	require.NoError(t, guard.Check(context.Background(), "https://example.com/hook"))
}

// A name that answers with both a public and a private address is the classic bypass.
func TestSSRFGuardRejectsAMixedAnswer(t *testing.T) {
	t.Parallel()

	guard := jobs.NewSSRFGuardWithResolver(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("10.0.0.7"),
		}, nil
	})

	err := guard.Check(context.Background(), "https://sneaky.example.com/hook")

	require.ErrorIs(t, err, jobs.ErrWebhookRejected)
}

func TestSSRFGuardRejectsAHostThatResolvesToNothing(t *testing.T) {
	t.Parallel()

	guard := jobs.NewSSRFGuardWithResolver(func(context.Context, string) ([]netip.Addr, error) {
		return nil, nil
	})

	require.ErrorIs(t,
		guard.Check(context.Background(), "https://void.example.com/hook"),
		jobs.ErrWebhookRejected)
}

func TestNoWebhookIsNotAnError(t *testing.T) {
	t.Parallel()

	require.NoError(t, jobs.NewSSRFGuard().Check(context.Background(), ""))
}
