# Observer Lazy Artifact Relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Implement observer-backed lazy artifact relay so driver file manifests work without agentserver peer proxy.

**Architecture:** Add artifact/write persistence to `observerstore`, HTTP endpoints to `observerweb`, and a driver-side relay client that registers handles, uploads requested file bytes on demand, and downloads completed writes during `wait_task`. Keep peer proxy as the default-compatible mode and add `observer_lazy` as a driver config option.

**Tech Stack:** Go 1.26, net/http, SQLite via modernc.org/sqlite, existing observer bearer-token auth, existing driver manifest structures.

---

### Task 1: Observer Artifact Store

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/observerstore/store.go`
- Test: `multi-agent/internal/observerstore/store_test.go`

- [x] Add tables for `artifacts`, `artifact_requests`, and `writes`.
- [x] Add store methods to create lazy artifacts, request pending content, store content, create write tokens, store write content, and list completed writes.
- [x] Verify with observerstore unit tests.

### Task 2: Observer HTTP API

**Files:**
- Modify: `multi-agent/internal/observerweb/server.go`
- Test: `multi-agent/internal/observerweb/server_test.go`

- [x] Add bearer-authenticated endpoints for `/api/artifacts`, `/api/artifacts/{id}`, `/api/artifacts/{id}/content`, `/api/artifact-requests`, `/api/write-tokens`, `/api/writes/{id}`, and `/api/writes`.
- [x] Verify lazy read returns `202`, driver upload changes it to `200`, and write upload/list works.

### Task 3: Driver Relay Client

**Files:**
- Modify: `multi-agent/internal/driver/config.go`
- Create: `multi-agent/internal/driver/observer_relay.go`
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/cmd/driver-agent/main.go`
- Test: `multi-agent/internal/driver/tools_test.go`

- [x] Add `driver_defaults.artifact_transport`.
- [x] Register observer lazy artifacts/writes during `submit_task` when configured.
- [x] Start a driver background loop that polls pending artifact requests and uploads bytes from local files.
- [x] Download completed observer writes during `wait_task` and write them to local files with existing overwrite validation.

### Task 4: Verification

**Files:**
- All touched Go files.

- [x] Run `go test ./internal/observerstore ./internal/observerweb ./internal/driver`.
- [x] Run `go test ./...`.
- [x] Run `go test -tags=contract ./tests/contract/...`.
- [x] Run `go vet ./...`.

