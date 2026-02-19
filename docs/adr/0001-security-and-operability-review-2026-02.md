# ADR 0001: Security and Operability Review (2026-02)

- Status: Accepted
- Date: 2026-02-19
- Deciders: driftd maintainers
- Context: post-release review of security and platform hardening gaps

## Context

A focused review identified issues across command execution safety, queue locking semantics, CSRF behavior in dev mode, insecure dev-mode guardrails, plan-output handling, clone behavior for large repos, and Helm production hardening knobs.

This ADR records what we will implement now vs defer.

## Decisions

1. Terraform plan-only wrapper safety (`internal/runner/binaries.go`)
- Decision: Keep current behavior short-term; replace shell wrapper with a Go-native arg parser/executor in a follow-up hardening pass.
- Rationale: Current wrapper is not trivially injectable due to quoted exec usage, but removing shell mediation is still a cleaner and safer long-term design.
- Priority: Medium

2. Stack inflight lock + enqueue atomicity (`internal/queue/stack_scan_queue.go`)
- Decision: Implement Redis Lua-based atomic enqueue for inflight lock + queue writes.
- Rationale: Current two-phase `SetNX` + pipeline flow can leave inconsistent state on partial failure.
- Priority: High

3. CSRF bypass scope in insecure dev mode (`internal/api/middleware.go`)
- Decision: Restrict CSRF bypass to localhost-only requests.
- Rationale: Current bypass on all non-HTTPS requests is too broad.
- Priority: High

4. Insecure dev mode runtime guard (`cmd/driftd/main.go`)
- Decision: Fail startup when `insecure_dev_mode=true` and `listen_addr` is non-local, with an explicit opt-out override for intentional demos.
- Rationale: Warning-only is too easy to miss; default should be safe.
- Priority: High

5. Plan output confidentiality (`internal/runner/redact.go`, `internal/storage/storage.go`)
- Decision: Keep plan output storage (required UX for stack detail page), and harden storage by encrypting plan output at rest. Continue best-effort redaction as defense-in-depth.
- Rationale: Disabling plan storage degrades core product UX and is not acceptable as the default behavior.
- Priority: High

6. Clone depth for large repositories
- Decision: Add configurable clone depth with shallow clone default.
- Rationale: We usually need current tree state, not full history.
- Priority: Medium

7. Helm infrastructure controls
- Decision: Provide optional, user-customizable `NetworkPolicy` and `PodDisruptionBudget`, disabled by default.
- Rationale: Security posture should be configurable per environment without forcing assumptions.
- Priority: Implemented in this change set

8. Redis defaults in Helm values
- Decision: Keep dev-oriented defaults in chart defaults, keep production guidance strict in examples/docs.
- Rationale: Preserve local quickstart ergonomics while documenting production expectations.
- Priority: Medium

9. SBOM generation
- Decision: Add SBOM generation to release pipeline as a follow-up enhancement.
- Rationale: Useful for supply-chain transparency/compliance, not a runtime blocker.
- Priority: Low/Medium

## Consequences

- driftd retains stack detail usefulness by continuing to store plan output.
- Security posture improves via stricter dev-mode boundaries and eventual at-rest encryption of plan artifacts.
- Helm chart remains easy to adopt while exposing production-oriented controls when needed.

## Implemented Now (this ADR cycle)

- Added optional/customizable Helm `NetworkPolicy` support, disabled by default.
- Added optional/customizable Helm `PodDisruptionBudget` support, disabled by default.
- Set Bitnami Redis subchart `networkPolicy`/`pdb` defaults to disabled so chart-default installs do not emit NP/PDB resources.
- Added chart docs/examples for both features.

## Follow-up Work Items

- Lua-based atomic enqueue for stack inflight lock + queue write path.
- Localhost-only CSRF bypass logic and tests.
- Insecure-dev-mode non-local bind startup guard.
- Encrypted plan-output at-rest storage with migration strategy.
- Configurable clone depth.
- SBOM generation in release workflow.
