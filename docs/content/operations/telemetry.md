---
title: Telemetry
description: Operate secured Prometheus metrics, JSON logs, and optional in-process OpenTelemetry tracing.
---

# Telemetry

The operator exposes secured controller-runtime Prometheus metrics, emits JSON logs to standard output, and can optionally export OpenTelemetry traces. Telemetry must never become a reconciliation or admission dependency.

## Logs

Standard output is the canonical log path:

```bash
kubectl logs \
  --namespace azure-workload-identity-operator-system \
  deployment/azure-workload-identity-operator-controller-manager \
  --container manager
```

When a valid span is active, sampled or unsampled logs include `trace_id` and `span_id`. The process does not use the OpenTelemetry Logs SDK and does not duplicate logs over OTLP.

## Metrics

Metrics are enabled by default on secured port 8443 and protected by Kubernetes authentication and authorization. The chart includes a metrics Service and optional monitoring resources in the repository configuration.

Prometheus labels remain bounded. Kubernetes names, UIDs, and Azure resource identifiers can appear in logs or traces for correlation but are not added to Prometheus labels.

## Enable tracing

Tracing is disabled by default and requires no OpenTelemetry Operator, sidecar, DaemonSet, or bundled Collector.

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

For OTLP/gRPC, use `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` and the receiver's gRPC endpoint, normally port 4317. Keep authorization headers in a Secret.

For a private CA:

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

The generic hooks reject replacement of fixed operator variables, the webhook certificate volume, Azure startup-scope volume, and their mount paths.

## Sampling and buffering

The default sampler is parent-based always-on. Supported standard sampler values are:

- `always_on` and `always_off`;
- `traceidratio`;
- `parentbased_always_on` and `parentbased_always_off`; and
- `parentbased_traceidratio`.

Ratio samplers require `OTEL_TRACES_SAMPLER_ARG` between 0 and 1.

The batch processor queues up to 2,048 spans, exports at most 512 per batch, flushes every five seconds, and limits export and shutdown flushes to five seconds. A full queue drops spans instead of blocking the operator. Export errors are rate-limited to one JSON log entry per minute.

Invalid tracing configuration logs one startup error and leaves the process running with tracing disabled. Telemetry is not part of readiness or liveness.

## Trace coverage

Root reconciliation spans:

```text
oidcissuer.reconcile
workloadidentity.reconcile
workloadidentityrecovery.reconcile
```

Additional instrumentation covers initial Kubernetes state reads, OIDC document generation, Kubernetes REST traffic, Azure SDK operations and retries, and validating admission handlers continuing W3C `traceparent`.

Custom attributes use the `azure_workload_identity_operator.*` namespace. Custom instrumentation never records request or response bodies, tokens, client secrets, authorization headers, federated subjects, or audiences.

## Network failure behavior

Allow egress to DNS and the selected OTLP endpoint when network policy is default-deny. An unreachable, slow, unauthorized, or invalid-TLS exporter must only cause rate-limited errors and dropped spans; reconciliation, admission, readiness, and liveness must remain healthy.
