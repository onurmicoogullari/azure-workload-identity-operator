# Telemetry

The operator keeps its existing secured controller-runtime Prometheus endpoint
and can optionally emit OpenTelemetry traces. Tracing is implemented in the
operator process and does not require the OpenTelemetry Operator, a sidecar, a
DaemonSet, or a bundled Collector. The installation owner supplies an OTLP
endpoint or leaves tracing disabled.

Logs are JSON on standard output. When a sampled or unsampled valid span is
active, log records include `trace_id` and `span_id`. The operator does not use
the OpenTelemetry Logs SDK, does not duplicate logs over OTLP, and does not
export custom OpenTelemetry metrics.

Standard output remains the canonical log path so `oc logs`, `kubectl logs`,
and cluster log collectors continue to work independently of the OTLP endpoint.
An in-process Logs SDK would duplicate those records unless standard output
were removed, and would add another exporter, queue, retry path, and failure
mode. It can be reconsidered if direct OTLP log export becomes an explicit
deployment requirement; trace correlation does not require it.

## Enable tracing

Tracing is disabled by default. Enable it and configure the exporter with
standard OpenTelemetry environment variables through the chart's generic
manager hooks:

```yaml
telemetry:
  tracing:
    enabled: true

manager:
  extraEnv:
    - name: OTEL_TRACES_EXPORTER
      value: otlp
    - name: OTEL_EXPORTER_OTLP_PROTOCOL
      value: http/protobuf
    - name: OTEL_EXPORTER_OTLP_ENDPOINT
      value: https://otel-gateway.example.net:4318
    - name: OTEL_EXPORTER_OTLP_HEADERS
      valueFrom:
        secretKeyRef:
          name: operator-otlp-credentials
          key: headers
```

For OTLP/gRPC, set `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` and use the receiver's
gRPC endpoint, normally port 4317. HTTP/protobuf is the default. Endpoint,
headers, compression, timeout, and per-trace overrides are handled by the
standard Go OTLP exporters. Keep authorization headers in a Secret, never in a
values file.

For a private CA, mount a Secret or ConfigMap and point the exporter at it:

```yaml
manager:
  extraEnv:
    - name: OTEL_EXPORTER_OTLP_CERTIFICATE
      value: /var/run/operator-otlp-ca/ca.crt
  extraVolumes:
    - name: operator-otlp-ca
      secret:
        secretName: operator-otlp-ca
  extraVolumeMounts:
    - name: operator-otlp-ca
      mountPath: /var/run/operator-otlp-ca
      readOnly: true
```

The hooks reject replacement of the operator's fixed environment variables,
the `webhook-certs` and Azure startup-scope volumes, and their mount paths.
Secret, ConfigMap, and projected volumes work with the OpenShift
`restricted-v2` SCC; hostPath and privileged telemetry mechanisms are
intentionally outside the supported contract.

## Sampling and buffering

The default sampler is parent-based always-on. The supported standard sampler
overrides are:

- `always_on`
- `always_off`
- `traceidratio`
- `parentbased_always_on`
- `parentbased_always_off`
- `parentbased_traceidratio`

Ratio samplers require `OTEL_TRACES_SAMPLER_ARG` from 0 through 1. Sampling is
head-based; the operator does not require or assume collector-side tail
sampling.

The batch processor has a non-blocking queue of 2,048 spans, exports at most
512 spans per batch, flushes every five seconds, and limits each export and
shutdown flush to five seconds. A full queue drops telemetry instead of
blocking admission or reconciliation. Export errors are rate-limited to one
JSON log entry per minute. Invalid tracing configuration emits one startup
error and leaves the operator running with tracing disabled. Telemetry is not
part of liveness or readiness.

## Trace coverage

Each controller invocation creates one of these root spans:

- `oidcissuer.reconcile`
- `workloadidentity.reconcile`
- `workloadidentityrecovery.reconcile`

The operator adds `kubernetes.state.observe` around the initial cached object
read and `oidc.documents.generate` around discovery/JWKS generation. The
instrumented Kubernetes REST transport covers direct API calls, cache watches,
leader-election Lease traffic, and uncached reads. The Azure SDK adapter covers
credential and service operations, long-running operations, HTTP attempts, and
SDK retries. Every controller-runtime validating webhook handler accepts and
continues W3C `traceparent` and records its admission outcome.

Custom attributes use the `azure_workload_identity_operator.*` namespace.
Reconcile and admission outcomes are bounded values. Kubernetes names and UIDs
and Azure resource identifiers may appear in spans and logs for incident
correlation but are never added to Prometheus labels. Request/response bodies,
tokens, client secrets, authorization headers, federated subjects, and
audiences are not recorded by custom instrumentation.

Resource identity is fixed by the process:

- `service.name=azure-workload-identity-operator`
- `service.version` from the release chart or Go build
- `service.instance.id` from the Pod UID
- `k8s.pod.name`, `k8s.pod.uid`, and `k8s.namespace.name`

Cluster, environment, region, and team metadata can be added through
`OTEL_RESOURCE_ATTRIBUTES`. Those values cannot replace the fixed service or
Pod identity.

## Network and platform operation

No Collector is assumed. If the namespace or cluster uses default-deny egress,
allow DNS and the selected OTLP endpoint in addition to the Kubernetes API and
Azure endpoints already required by the operator. An unreachable OTLP endpoint
must only produce rate-limited exporter errors and dropped spans.

OpenShift 4.22.8 is a required production compatibility target, not a minimum
supported version. Older OpenShift releases are not excluded by this policy.
OpenShift 4.22 is based on Kubernetes 1.35, while the current
controller-runtime/client-go build tracks Kubernetes 1.36. The operator uses
their shared stable APIs, but the exact candidate must pass a focused 4.22.8
smoke test under the target cluster's SCC, proxy, trust, DNS, and egress
policies before it is used in that environment.

That smoke test must verify:

1. normal reconciliation, recovery, webhooks, and leader election with tracing
   disabled;
2. correlated traces and JSON logs over both OTLP/HTTP and OTLP/gRPC when each
   transport is selected;
3. admission under the actual SCC with arbitrary assigned UID and any CA or
   credential mounts;
4. unchanged reconciliation, admission, readiness, and liveness with an
   unreachable, slow, unauthorized, or invalid-TLS exporter; and
5. a Helm rollback to tracing disabled.

The existing OpenShift 4.21 CRC path remains useful regression coverage but
does not satisfy the 4.22.8 release gate. Re-run the focused telemetry tests
when the Go version, controller-runtime/client-go, OpenTelemetry SDK/contrib,
or Azure SDK tracing adapter is upgraded. Keep all telemetry modules pinned and
review their release notes together before updating them.
