---
title: Architecture decision records
description: Index of accepted architecture decisions that define the operator's safety and ownership model.
---

# Architecture decision records

Architecture decision records (ADRs) explain why durable constraints exist. They describe one decision each and are updated by superseding records rather than by rewriting history.

| ADR | Status | Decision |
| --- | --- | --- |
| [0001](./0001-single-azure-scope.md) | Accepted | Bind one operator installation to one retained Azure scope. |
| [0002](./0002-retain-by-default.md) | Accepted | Retain external identity resources by default and verify ownership before deletion. |
| [0003](./0003-controlled-recovery.md) | Accepted | Transfer retained managed identities only through a separate forward-only recovery API. |

New ADRs use the next four-digit sequence and include status, date, context, decision, alternatives, and consequences.
