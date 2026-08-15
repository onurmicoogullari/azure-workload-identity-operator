/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	aztracing "github.com/Azure/azure-sdk-for-go/sdk/azcore/tracing"
	"github.com/Azure/azure-sdk-for-go/sdk/tracing/azotel"
	"github.com/go-logr/logr"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	// InstrumentationName identifies spans created directly by this operator.
	InstrumentationName = "github.com/onurmicoogullari/azure-workload-identity-operator"
	serviceName         = "azure-workload-identity-operator"

	defaultMaxQueueSize            = 2048
	defaultMaxExportBatchSize      = 512
	defaultBatchTimeout            = 5 * time.Second
	defaultExportTimeout           = 5 * time.Second
	errorLogInterval               = time.Minute
	parentBasedTraceIDRatioSampler = "parentbased_traceidratio"
)

// Config contains the operator-owned tracing configuration. Exporter and
// resource configuration otherwise use standard OpenTelemetry environment
// variables.
type Config struct {
	Enabled        bool
	ServiceVersion string
	PodName        string
	PodUID         string
	PodNamespace   string
}

// Runtime owns the process-wide OpenTelemetry SDK configured for the operator.
// Its zero value is disabled and safe to use.
type Runtime struct {
	enabled           bool
	tracerProvider    trace.TracerProvider
	sdkTracerProvider *sdktrace.TracerProvider
	propagator        propagation.TextMapPropagator
	azureProvider     aztracing.Provider
}

// New configures tracing when explicitly enabled. Configuration errors return
// a disabled runtime so telemetry can never prevent the operator from starting.
func New(ctx context.Context, config Config) (*Runtime, error) {
	disabled := &Runtime{
		tracerProvider: noop.NewTracerProvider(),
		propagator:     propagation.TraceContext{},
	}
	if !config.Enabled {
		return disabled, nil
	}

	sampler, err := samplerFromEnvironment()
	if err != nil {
		return disabled, err
	}
	exporter, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return disabled, fmt.Errorf("configure trace exporter: %w", err)
	}

	attributes := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
		attribute.String("service.version", valueOrUnknown(config.ServiceVersion)),
	}
	if config.PodUID != "" {
		attributes = append(attributes,
			attribute.String("service.instance.id", config.PodUID),
			attribute.String("k8s.pod.uid", config.PodUID),
		)
	}
	if config.PodName != "" {
		attributes = append(attributes, attribute.String("k8s.pod.name", config.PodName))
	}
	if config.PodNamespace != "" {
		attributes = append(attributes, attribute.String("k8s.namespace.name", config.PodNamespace))
	}

	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		// Operator-owned identity is applied last so OTEL_RESOURCE_ATTRIBUTES
		// cannot replace service or pod identity.
		resource.WithAttributes(attributes...),
	)
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultExportTimeout)
		defer cancel()
		_ = exporter.Shutdown(shutdownCtx)
		return disabled, fmt.Errorf("configure trace resource: %w", err)
	}

	processor := sdktrace.NewBatchSpanProcessor(
		exporter,
		sdktrace.WithMaxQueueSize(defaultMaxQueueSize),
		sdktrace.WithMaxExportBatchSize(defaultMaxExportBatchSize),
		sdktrace.WithBatchTimeout(defaultBatchTimeout),
		sdktrace.WithExportTimeout(defaultExportTimeout),
	)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithSpanProcessor(processor),
	)
	propagator := propagation.TraceContext{}
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagator)
	otel.SetErrorHandler(&rateLimitedErrorHandler{logger: logr.FromContextOrDiscard(ctx)})

	return &Runtime{
		enabled:           true,
		tracerProvider:    provider,
		sdkTracerProvider: provider,
		propagator:        propagator,
		azureProvider:     azotel.NewTracingProvider(provider, nil),
	}, nil
}

// Enabled reports whether the SDK was initialized successfully.
func (r *Runtime) Enabled() bool {
	return r != nil && r.enabled
}

// TracerProvider returns the configured provider or a no-op provider.
func (r *Runtime) TracerProvider() trace.TracerProvider {
	if r == nil || r.tracerProvider == nil {
		return noop.NewTracerProvider()
	}
	return r.tracerProvider
}

// Propagator returns the W3C Trace Context propagator used by the runtime.
func (r *Runtime) Propagator() propagation.TextMapPropagator {
	if r == nil || r.propagator == nil {
		return propagation.TraceContext{}
	}
	return r.propagator
}

// AzureTracingProvider adapts this runtime for Azure SDK clients.
func (r *Runtime) AzureTracingProvider() aztracing.Provider {
	if r == nil {
		return aztracing.Provider{}
	}
	return r.azureProvider
}

// Shutdown flushes buffered spans within the caller's deadline.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.sdkTracerProvider == nil {
		return nil
	}
	return r.sdkTracerProvider.Shutdown(ctx)
}

func samplerFromEnvironment() (sdktrace.Sampler, error) {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER")))
	switch name {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "traceidratio", parentBasedTraceIDRatioSampler:
		ratio, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("OTEL_TRACES_SAMPLER_ARG")), 64)
		if err != nil || ratio < 0 || ratio > 1 {
			return nil, fmt.Errorf("OTEL_TRACES_SAMPLER_ARG must be a number from 0 to 1 for %s", name)
		}
		ratioSampler := sdktrace.TraceIDRatioBased(ratio)
		if name == parentBasedTraceIDRatioSampler {
			return sdktrace.ParentBased(ratioSampler), nil
		}
		return ratioSampler, nil
	default:
		return nil, fmt.Errorf("unsupported OTEL_TRACES_SAMPLER value %q", name)
	}
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

type rateLimitedErrorHandler struct {
	logger logr.Logger

	mu       sync.Mutex
	lastTime time.Time
}

func (h *rateLimitedErrorHandler) Handle(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.lastTime) < errorLogInterval {
		return
	}
	h.lastTime = time.Now()
	h.logger.Error(err, "OpenTelemetry exporter failed; operator processing is unaffected")
}
