package http

import "github.com/pcaokhai/vitts/gateway/internal/degrade"

// RateLimitWithBreaker lets a test drive the degrade window from a fake clock instead of
// waiting a minute for it to close.
var RateLimitWithBreaker = rateLimit

// NewBreaker is degrade.New, re-exported so the test does not need its own import.
var NewBreaker = degrade.New
