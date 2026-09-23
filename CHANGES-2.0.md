# Komari 2.0

This release requires coordinated server, frontend, Emerald and Agent deployment.

- Agent v2 is the sole protocol. Removed v1 report/basic-info/task/ping routes and RPC methods, connection negotiation and protocol markers. HTTP presence expires normally; WebSocket replacement and HTTP command recovery remain supported.
- Removed `/api/clients` live WebSocket, `/api/recent/:uuid`, `/api/records/load`, `/api/records/ping`, `common:getRecords` and their public RPC wrappers, reconstruction and sampling algorithms. Live charts use `common:getNodeRecentStatus`; historical charts use `public:queryMetrics`.
- Metrics always return `point_format: "points_v1"` and `[unix_ms, value_or_null, count, optional_labels]`. Removed compact opt-in, duplicate field aliases and the ordinary-object response. Retained parameters: metric_key/metric_keys, entity_id/entity_ids, start/end/hours, tags, fill_empty, max_points/max_points_by_metric, aggregation/aggregation_by_metric.
- Ping statistics merge retained source distributions over the entire window. P50/P95/P99 use weighted ranks of the merged sketch; means and population variance use full-window moments, removing -1 failure samples via paired loss counts. Raw percentile queries use the same nearest-rank convention. No bucket percentile averaging or secondary browser percentile calculation remains.
- Percentiles remain sketch estimates (`quantiles_approximate`). The returned interval identifies retained source resolution. Historical rollups cannot recover an exact successful minimum when a bucket also contained failures: minimum is omitted in that case. Latest is omitted when the last probe failed. Missing/mismatched loss history marks `loss_approximate` and does not fabricate latency moments.
- Removed unused Ping history scans from the live status response. Emerald 2.0 and traffic-report 0.2.0 consume the canonical metric format.

Existing metric data, node configuration, authentication, remote-control opt-outs and MOTD cleanup are preserved. One-time data import/recovery and current network/OS recovery remain necessary operations, not retired protocol paths.

Validation: full Go tests; race checks on metric storage, agent state, ingest and RPC; frontend transport/onboarding tests and production build; Emerald lint/typecheck/build; traffic-report typecheck, isolated tuple aggregation tests and ZIP build. Deploy Agents before the server, and install matching UI/plugin packages with the server update.
