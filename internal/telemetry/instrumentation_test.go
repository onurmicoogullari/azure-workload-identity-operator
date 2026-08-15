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
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	workloadidentityv1alpha1 "github.com/onurmicoogullari/azure-workload-identity-operator/api/v1alpha1"
)

const (
	testResourceName      = "identity"
	testResourceNamespace = "application"
	testResourceUID       = "identity-uid"
)

func TestReconcilerRecordsBoundedOutcomeAndIdentity(t *testing.T) {
	runtime, exporter := testRuntime(t)
	reconciler := runtime.WrapReconciler(
		"workloadidentity.reconcile",
		"WorkloadIdentity",
		reconcile.Func(func(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
			SetResourceAttributes(ctx, "WorkloadIdentity", &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
				Name:      testResourceName,
				Namespace: testResourceNamespace,
				UID:       types.UID(testResourceUID),
			}})
			tracer := runtime.TracerProvider().Tracer(InstrumentationName)
			_, stateSpan := tracer.Start(ctx, "kubernetes.state.observe")
			stateSpan.End()
			_, postReadSpan := tracer.Start(ctx, "post-read.operation")
			postReadSpan.End()
			SetReconcileOutcome(ctx, "blocked")
			return reconcile.Result{RequeueAfter: 1}, nil
		}),
	)

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: testResourceNamespace, Name: testResourceName},
	})
	if err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(spans))
	}
	root := spanByName(t, spans, "workloadidentity.reconcile")
	assertAttribute(t, root.Attributes, AttrReconcileOutcome, "blocked")
	assertAttribute(t, root.Attributes, AttrResourceKind, "WorkloadIdentity")
	assertAttribute(t, root.Attributes, AttrResourceName, testResourceName)
	assertAttribute(t, root.Attributes, AttrResourceNamespace, testResourceNamespace)
	assertAttribute(t, root.Attributes, AttrResourceUID, testResourceUID)

	for _, childName := range []string{"kubernetes.state.observe", "post-read.operation"} {
		child := spanByName(t, spans, childName)
		if child.Parent.SpanID() != root.SpanContext.SpanID() {
			t.Fatalf("parent of %s = %s, want reconcile root %s", childName, child.Parent.SpanID(), root.SpanContext.SpanID())
		}
	}
}

func TestReconcilerInfersRequeueOutcome(t *testing.T) {
	runtime, exporter := testRuntime(t)
	reconciler := runtime.WrapReconciler(
		"oidcissuer.reconcile",
		"OIDCIssuer",
		reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{RequeueAfter: 1}, nil
		}),
	)

	if _, err := reconciler.Reconcile(context.Background(), reconcile.Request{}); err != nil {
		t.Fatal(err)
	}

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	assertAttribute(t, spans[0].Attributes, AttrReconcileOutcome, "requeue")
}

func TestWebhookServerContinuesTraceparent(t *testing.T) {
	runtime, exporter := testRuntime(t)
	base := webhook.NewServer(webhook.Options{})
	server := runtime.WrapWebhookServer(base)
	scheme := k8sruntime.NewScheme()
	if err := workloadidentityv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	validator := &recordingWorkloadIdentityValidator{}
	server.Register("/validate", admission.WithValidator(scheme, validator))

	object := &workloadidentityv1alpha1.WorkloadIdentity{
		TypeMeta: metav1.TypeMeta{
			APIVersion: workloadidentityv1alpha1.GroupVersion.String(),
			Kind:       "WorkloadIdentity",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      testResourceName,
			Namespace: testResourceNamespace,
			UID:       types.UID(testResourceUID),
		},
	}
	objectJSON, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	reviewJSON, err := json.Marshal(admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: admissionv1.SchemeGroupVersion.String(), Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID:       types.UID("request-uid"),
			Kind:      metav1.GroupVersionKind{Group: workloadidentityv1alpha1.GroupVersion.Group, Version: workloadidentityv1alpha1.GroupVersion.Version, Kind: "WorkloadIdentity"},
			Resource:  metav1.GroupVersionResource{Group: workloadidentityv1alpha1.GroupVersion.Group, Version: workloadidentityv1alpha1.GroupVersion.Version, Resource: "workloadidentities"},
			Name:      object.Name,
			Namespace: object.Namespace,
			Operation: admissionv1.Create,
			Object:    k8sruntime.RawExtension{Raw: objectJSON},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "https://operator.example/validate", bytes.NewReader(reviewJSON))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	base.WebhookMux().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := validator.spanContext.TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace ID = %q", got)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	span := spanByName(t, spans, "POST /validate")
	if got := span.Parent.SpanID().String(); got != "00f067aa0ba902b7" {
		t.Fatalf("parent span ID = %q", got)
	}
	assertAttribute(t, span.Attributes, AttrAdmissionOutcome, "allowed")
	assertAttribute(t, span.Attributes, AttrResourceUID, testResourceUID)
}

type recordingWorkloadIdentityValidator struct {
	spanContext trace.SpanContext
}

func (v *recordingWorkloadIdentityValidator) ValidateCreate(
	ctx context.Context,
	object *workloadidentityv1alpha1.WorkloadIdentity,
) (admission.Warnings, error) {
	v.spanContext = trace.SpanContextFromContext(ctx)
	RecordAdmissionOutcome(ctx, "WorkloadIdentity", object, nil)
	return nil, nil
}

func (*recordingWorkloadIdentityValidator) ValidateUpdate(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
	*workloadidentityv1alpha1.WorkloadIdentity,
) (admission.Warnings, error) {
	return nil, nil
}

func (*recordingWorkloadIdentityValidator) ValidateDelete(
	context.Context,
	*workloadidentityv1alpha1.WorkloadIdentity,
) (admission.Warnings, error) {
	return nil, nil
}

func testRuntime(t *testing.T) (*Runtime, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
	return &Runtime{
		enabled:        true,
		tracerProvider: provider,
		propagator:     propagation.TraceContext{},
	}, exporter
}

func assertAttribute(t *testing.T, attributes []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, item := range attributes {
		if string(item.Key) == key {
			if got := item.Value.AsString(); got != want {
				t.Fatalf("attribute %s = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("attribute %s not found", key)
}

func spanByName(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span %q not found", name)
	return tracetest.SpanStub{}
}
