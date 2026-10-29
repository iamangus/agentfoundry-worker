# Agent Foundry worker

The worker executes Foundry agent runs on Temporal. Foundry owns agent
definitions, provider configuration, MCP connections, authentication, the
API-facing run index, and streamed results. The worker resolves a definition
through Foundry at execution time, then performs LLM and tool activities.
The JSON projection is documented in [Foundry's definition contract](https://github.com/iamangus/agentfoundry/blob/main/docs/agent-definition-contract.md).

## Run types

- `RunAgentWorkflow` handles one request and returns a result.
- `PersistentRunWorkflow` accepts input signals and maintains conversation
  context across turns. Accepted input IDs are deduplicated in workflow state.
  It rolls over with Temporal Continue-As-New every 20 completed turns or
  5,000 history events,
  retaining the most recent 120 messages within a 256 KiB history budget and
  the most recent 1,024 input IDs. Eve owns the complete visible transcript.

The worker publishes turn results to Foundry, which persists the status and
response before notifying SSE subscribers. Foundry can reattach to an existing
Temporal workflow after a restart.

## Build and verify

```sh
go test ./...
go vet ./...
go build -o worker ./cmd/worker/
```

Go 1.25.3 is required. Run with `worker.yaml` in the working directory, or
configure `TEMPORAL_HOST_PORT`, `TEMPORAL_NAMESPACE`, `TEMPORAL_API_KEY`,
`ORCHESTRATOR_URL`, `ORCHESTRATOR_API_KEY`, and optional `GRAPHITI_URL` through
the environment. `ORCHESTRATOR_API_KEY` authorizes the worker's requests to
Foundry. Model providers and agent definitions are configured in Foundry.

## Backup inference

An agent can declare `backup_provider_id` and optional `backup_model` (empty uses
the backup provider's default). Foundry resolves credentials server-side; the
worker selects only primary or backup, never arbitrary provider credentials.

For configured backups, Temporal owns a two-attempt primary retry budget and a
two-attempt backup budget. Rate limits, connection errors, timeouts, and provider
5xx errors are eligible. Invalid requests, authentication failures, and
cancellations do not switch providers. Once switched, the backup is retained
through tool calls and steering within the current turn. Each new persistent
turn retries the primary, including when older executions carried backup state
through Continue-As-New. A recovered provider or corrected URL therefore takes
effect on the next turn without resetting the conversation. Child agents use
their own definition. Existing workflows use the original behavior via the
`backup-inference-v1` version marker. Agents without backups retain their prior
retry policy.

Per-turn `persistent-primary-per-turn-v1/` version markers preserve replay of
old turns while enabling primary recovery on the next new input in an existing
execution. Persistent rollovers no longer carry a turn's failover into new work.

Switching retries only inference, not completed tools. Stream retries discard
only the incomplete response. Provider/model IDs, attempts, and switch reasons
are available in Foundry's execution trace. Deploy the Foundry API with backup
routing before enabling backups on the updated worker/UI.
