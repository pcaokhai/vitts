package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ServiceName identifies the gateway in traces.
const ServiceName = "vitts-gateway"

const exporterShutdownTimeout = 5 * time.Second

// Shutdown flushes and stops whatever Tracing installed. Always non-nil.
type Shutdown func(context.Context) error

// Tracing installs a global tracer provider and propagator.
//
// An empty endpoint installs propagation only, with no exporter and no background
// batcher: local runs and tests should not need a collector, and a gateway that cannot
// reach its collector must still serve traffic.
func Tracing(ctx context.Context, endpoint string) (Shutdown, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return func(context.Context) error { return nil }, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(ServiceName),
	))
	if err != nil {
		return func(context.Context) error { return nil }, fmt.Errorf("otel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, exporterShutdownTimeout)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			return fmt.Errorf("tracer provider shutdown: %w", err)
		}
		return nil
	}, nil
}
