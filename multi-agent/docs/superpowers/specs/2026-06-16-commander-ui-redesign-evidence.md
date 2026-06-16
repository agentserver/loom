# Commander UI Redesign Evidence

- Date: 2026-06-16
- Branch: commander-ui-redesign-design

## Verification

| Command | Result |
|---|---|
| `go test ./internal/commander ./internal/commanderhub ./pkg/agentbackend/... -count=1` | PASS |
| `cd internal/commanderhub/webapp && npm test` | PASS, 5 files / 14 tests |
| `cd internal/commanderhub/webapp && npm run build` | PASS |
| `cd internal/commanderhub/webapp && npm run e2e` | PASS, 4 Playwright tests |
| `git diff --exit-code internal/commanderhub/assets/dist` | PASS |
| `go test -race ./internal/commanderhub -run 'TestHTTP_TurnRejectsConcurrentSameSession\|TestProxy_SendCommandStreamTurn\|TestHub_AcksRegisterAndAdmitsDaemon' -count=1` | PASS |

## Manual UI Notes

- `/commander` renders the three-pane React/Vite workbench through Go embed.
- Turn lifecycle status appears outside assistant message content.
- The composer is disabled while a session turn is queued, starting, answering, or awaiting approval.
- File browsing is rooted at the selected session working directory.
- File preview refuses content larger than 2MB.
