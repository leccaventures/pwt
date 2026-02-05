# Metrics Reference

Metrics are exposed on the Prometheus endpoint (`/metrics`) on the **Prometheus port** (`advanced.prometheus.port`).
If the Prometheus port matches the dashboard port, the endpoint is served from the same server.

The metric prefix is configurable via `advanced.prometheus.metrics_prefix` (default: `pharos`).

## Block time
- **`${prefix}_block_time_seconds`** (`chain_id`)
  - Last block interval in seconds.
- **`${prefix}_block_time_avg_100_seconds`** (`chain_id`)
  - Average block time over the last 100 blocks in seconds.

## Validator metrics
All validator metrics include labels: `chain_id`, `validator`, `moniker`.

- **`${prefix}_validator_uptime_ratio`**
  - Uptime ratio (0–1) over the rolling window.
- **`${prefix}_validator_missed_blocks_total`**
  - Missed blocks in the current window.
- **`${prefix}_validator_total_blocks_window`**
  - Total blocks in the current window.
- **`${prefix}_validator_down`**
  - Down status (1=down, 0=up).
- **`${prefix}_validator_staking`**
  - Staking amount.
- **`${prefix}_validator_last_height`**
  - Last height where the validator participated.
- **`${prefix}_validator_status`**
  - Validator status code.
- **`${prefix}_validator_last_seen_timestamp`**
  - Unix timestamp of last signed block.
- **`${prefix}_validator_last_signed`**
  - Whether the validator signed the last block (1=yes, 0=no).
- **`${prefix}_validator_last_missed`**
  - Whether the validator missed the last block (1=yes, 0=no).
- **`${prefix}_validator_last_signed_block`**
  - Height of last signed block.
- **`${prefix}_validator_last_missed_block`**
  - Height of last missed block.

## Node metrics
All node metrics include labels: `chain_id`, `label`, `rpc_url`.

- **`${prefix}_node_height`**
  - Current node height.
- **`${prefix}_node_up`**
  - Node up status (1=up, 0=down).
- **`${prefix}_node_syncing`**
  - Node syncing status (1=syncing, 0=synced).
  - **Note:** currently derived from RPC `SyncProgress` and may be refined later.
- **`${prefix}_node_last_check_timestamp`**
  - Unix timestamp of last node check.
