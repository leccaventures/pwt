# API Reference

All API and WebSocket endpoints are served from the **dashboard port** (`advanced.dashboard_port`).

## HTTP API

### GET /api/state
Combined state payload.

**Response**
```json
{
  "avg_block_time": 920.5,
  "validators": [
    {
      "id": "0x...",
      "moniker": "LECCA",
      "status": 1,
      "missed": 12,
      "total": 342,
      "uptime": 0.9649,
      "last_height": 12698196,
      "last_seen": "2026-02-05T04:55:12Z",
      "down": false,
      "staking": "100000",
      "window": [1,1,0,2]
    }
  ],
  "nodes": [
    {
      "label": "LECCA",
      "rpc_url": "http://...",
      "ws_url": "ws://...",
      "healthy": true,
      "block_height": 12698196,
      "syncing": false,
      "latency": "120ms",
      "last_error": "",
      "last_check": "2026-02-05T04:55:12Z"
    }
  ]
}
```

Notes:
- `avg_block_time` is **milliseconds**.
- `window` values: `1 = signed`, `0 = missed`, `2 = pending/unknown`.
- Window order is **oldest → newest** for the last N heights.

### GET /api/validators
Validator list **without** window bitmap.

**Response**
```json
[
  {
    "id": "0x...",
    "moniker": "LECCA",
    "status": 1,
    "missed": 12,
    "total": 342,
    "uptime": 0.9649,
    "last_height": 12698196,
    "last_seen": "2026-02-05T04:55:12Z",
    "down": false,
    "staking": "100000"
  }
]
```

### GET /api/validators/windows
Window bitmap only.

**Response**
```json
[
  {
    "id": "0x...",
    "moniker": "LECCA",
    "window": [1,1,0,2]
  }
]
```

### GET /api/nodes
Node status list.

**Response**
```json
[
  {
    "label": "LECCA",
    "rpc_url": "http://...",
    "ws_url": "ws://...",
    "healthy": true,
    "block_height": 12698196,
    "syncing": false,
    "latency": "120ms",
    "last_error": "",
    "last_check": "2026-02-05T04:55:12Z"
  }
]
```

### GET /api/blocktime
Average block time only.

**Response**
```json
{
  "avg_block_time": 920.5
}
```

## WebSocket

### /ws
The dashboard uses WebSocket for real-time updates. Messages are JSON with a `type` field.

#### type: validators
```json
{
  "type": "validators",
  "validators": [
    {
      "id": "0x...",
      "moniker": "LECCA",
      "status": 1,
      "missed": 12,
      "total": 342,
      "uptime": 0.9649,
      "last_height": 12698196,
      "last_seen": "2026-02-05T04:55:12Z",
      "down": false,
      "staking": "100000",
      "window": [1,1,0,2]
    }
  ]
}
```

#### type: nodes
```json
{
  "type": "nodes",
  "nodes": [
    {
      "label": "LECCA",
      "rpc_url": "http://...",
      "ws_url": "ws://...",
      "healthy": true,
      "block_height": 12698196,
      "syncing": false,
      "latency": "120ms",
      "last_error": "",
      "last_check": "2026-02-05T04:55:12Z"
    }
  ]
}
```

#### type: blocktime
```json
{
  "type": "blocktime",
  "avg_block_time": 920.5
}
```

#### type: log
```json
{
  "type": "log",
  "timestamp": "12:34:56",
  "level": "INFO",
  "component": "PROC",
  "message": "Block #123 | Proof Fetched via ..."
}
```

#### type: config
```json
{
  "type": "config",
  "hide_logs": false
}
```
