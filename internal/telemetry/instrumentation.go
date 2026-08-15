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
	"errors"
	"net/http"
	"sync"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	AttrReconcileOutcome  = "azure_workload_identity_operator.reconcile.outcome"
	AttrResourceKind      = "azure_workload_identity_operator.resource.kind"
	AttrResourceName      = "azure_workload_identity_operator.resource.name"
	AttrResourceNamespace = "azure_workload_identity_operator.resource.namespace"
	AttrResourceUID       = "azure_workload_identity_operator.resource.uid"
	AttrAdmissionOutcome  = "azure_workload_identity_operator.admission.outcome"
)

type reconcileOutcomeState struct {
	mu      sync.Mutex
	outcome string
}

type reconcileOutcomeKey struct{}

type tracedReconciler struct {
	spanName string
	kind     string
	delegate reconcile.Reconciler
	provider trace.TracerProvider
}

// WrapReconciler creates one root span per reconciliation without changing the
// controller's retry or queue behavior.
func (r *Runtime) WrapReconciler(spanName, kind string, delegate reconcile.Reconciler) reconcile.Reconciler {
	if r == nil || !r.Enabled() {
		return delegate
	}
	return &tracedReconciler{
		spanName: spanName,
		kind:     kind,
		delegate: delegate,
		provider: r.TracerProvider(),
	}
}

func (r *tracedReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	attributes := []attribute.KeyValue{
		attribute.String(AttrResourceKind, r.kind),
		attribute.String(AttrResourceName, request.Name),
	}
	if request.Namespace != "" {
		attributes = append(attributes, attribute.String(AttrResourceNamespace, request.Namespace))
	}
	ctx, span := r.provider.Tracer(InstrumentationName).Start(
		ctx,
		r.spanName,
		trace.WithAttributes(attributes...),
	)
	defer span.End()
	ctx = ContextWithTraceLogger(ctx)
	state := &reconcileOutcomeState{}
	ctx = context.WithValue(ctx, reconcileOutcomeKey{}, state)

	result, err := r.delegate.Reconcile(ctx, request)
	outcome := state.value()
	if err != nil {
		outcome = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, "reconciliation failed")
	} else if outcome == "" && !result.IsZero() {
		outcome = "requeue"
	} else if outcome == "" {
		outcome = "success"
	}
	span.SetAttributes(attribute.String(AttrReconcileOutcome, outcome))
	return result, err
}

// SetReconcileOutcome records a more specific bounded outcome such as blocked
// or noop for the active reconcile span.
func SetReconcileOutcome(ctx context.Context, outcome string) {
	state, ok := ctx.Value(reconcileOutcomeKey{}).(*reconcileOutcomeState)
	if !ok {
		return
	}
	state.set(outcome)
}

func (s *reconcileOutcomeState) set(outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcome = outcome
}

func (s *reconcileOutcomeState) value() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome
}

// SetResourceAttributes enriches an active reconcile or admission span after
// the Kubernetes object has been decoded.
func SetResourceAttributes(ctx context.Context, kind string, object metav1.Object) {
	attributes := []attribute.KeyValue{
		attribute.String(AttrResourceKind, kind),
		attribute.String(AttrResourceName, object.GetName()),
	}
	if object.GetNamespace() != "" {
		attributes = append(attributes, attribute.String(AttrResourceNamespace, object.GetNamespace()))
	}
	if object.GetUID() != "" {
		attributes = append(attributes, attribute.String(AttrResourceUID, string(object.GetUID())))
	}
	trace.SpanFromContext(ctx).SetAttributes(attributes...)
}

// RecordAdmissionOutcome records only the bounded admission decision. API
// validation failures are denials; infrastructure and unexpected failures are
// errors.
func RecordAdmissionOutcome(ctx context.Context, kind string, object metav1.Object, err error) {
	SetResourceAttributes(ctx, kind, object)
	span := trace.SpanFromContext(ctx)
	if err == nil {
		span.SetAttributes(attribute.String(AttrAdmissionOutcome, "allowed"))
		return
	}

	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code >= 400 && status.Status().Code < 500 {
		span.SetAttributes(attribute.String(AttrAdmissionOutcome, "denied"))
		return
	}
	span.SetAttributes(attribute.String(AttrAdmissionOutcome, "error"))
	span.RecordError(err)
	span.SetStatus(codes.Error, "admission evaluation failed")
}

// ContextWithTraceLogger adds W3C trace identifiers to structured logs when a
// valid span is active.
func ContextWithTraceLogger(ctx context.Context) context.Context {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return ctx
	}
	logger := log.FromContext(ctx).WithValues(
		"trace_id", spanContext.TraceID().String(),
		"span_id", spanContext.SpanID().String(),
	)
	return log.IntoContext(ctx, logger)
}

// WrapRESTConfig instruments Kubernetes API HTTP calls while preserving any
// transport wrappers already registered on the config.
func (r *Runtime) WrapRESTConfig(config *rest.Config) {
	if r == nil || !r.Enabled() || config == nil {
		return
	}
	config.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(
			next,
			otelhttp.WithTracerProvider(r.TracerProvider()),
			otelhttp.WithPropagators(r.Propagator()),
			otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
				return "kubernetes.api " + request.Method
			}),
		)
	})
}

// WrapWebhookServer instruments every handler registered through
// controller-runtime's webhook builder.
func (r *Runtime) WrapWebhookServer(server webhook.Server) webhook.Server {
	if r == nil || !r.Enabled() || server == nil {
		return server
	}
	return &instrumentedWebhookServer{
		Server:     server,
		provider:   r.TracerProvider(),
		propagator: r.Propagator(),
	}
}

type instrumentedWebhookServer struct {
	webhook.Server
	provider   trace.TracerProvider
	propagator propagation.TextMapPropagator
}

func (s *instrumentedWebhookServer) Register(path string, handler http.Handler) {
	if admissionWebhook, ok := handler.(*admission.Webhook); ok {
		delegate := admissionWebhook.Handler
		admissionWebhook.Handler = admission.HandlerFunc(func(ctx context.Context, request admission.Request) admission.Response {
			return delegate.Handle(ContextWithTraceLogger(ctx), request)
		})
	}
	correlated := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx := ContextWithTraceLogger(request.Context())
		handler.ServeHTTP(writer, request.WithContext(ctx))
	})
	traced := otelhttp.NewHandler(
		correlated,
		"admission "+path,
		otelhttp.WithTracerProvider(s.provider),
		otelhttp.WithPropagators(s.propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return request.Method + " " + path
		}),
	)
	s.Server.Register(path, traced)
}
