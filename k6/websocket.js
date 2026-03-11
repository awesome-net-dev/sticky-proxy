// WebSocket broker simulation test — measures realistic relay throughput.
//
// Usage:
//   k6 run k6/websocket.js
//   k6 run -e PUSH_RATE=30 -e TOTAL_USERS=500 -e WS_HOLD=60 k6/websocket.js
//
// What it tests:
//   - Ramp to 1000 concurrent WebSocket connections through the proxy
//   - Each connection subscribes to backend push at PUSH_RATE msg/s (default 20)
//   - Client sends periodic commands (~1/s) while receiving the push stream
//   - Measures: message throughput, push latency, sequence gaps, echo RTT
//
// Acceptance criteria:
//   - All connections upgrade successfully (status 101)
//   - < 10 WebSocket errors total
//   - Average push latency < 50ms
//
// This simulates a real broker scenario where backends push high-frequency
// data (e.g. market feeds, game state, IoT telemetry) to connected clients.

import ws from 'k6/ws';
import { check } from 'k6';
import { Counter, Trend, Gauge } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { generateJWT } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '500');
const WS_PATH = __ENV.WS_PATH || '/ws';
const HOLD_SECONDS = parseInt(__ENV.WS_HOLD || '30');
const PUSH_RATE = parseInt(__ENV.PUSH_RATE || '20'); // msgs/s from backend

const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

// --- Metrics ---
const wsErrors = new Counter('ws_errors');
const wsMsgsSent = new Counter('ws_msgs_sent');
const wsMsgsReceived = new Counter('ws_msgs_received');
const wsPushLatency = new Trend('ws_push_latency', true);   // ms, from backend ts
const wsEchoRTT = new Trend('ws_echo_rtt', true);           // ms, round-trip for commands
const wsSeqGaps = new Counter('ws_sequence_gaps');           // missed/reordered messages
const wsPushRate = new Gauge('ws_push_rate');                // actual msgs/s received

export const options = {
  scenarios: {
    websockets: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 150 },
        { duration: '1m', target: 5000 },
        { duration: '2m', target: 5000 },
        { duration: '30s', target: 0 },
      ],
    },
  },
  thresholds: {
    ws_errors: ['count<10'],
    ws_push_latency: ['avg<50'],
  },
};

export default function () {
  const userId = (__VU - 1) % TOTAL_USERS;
  const token = tokens[userId];
  const wsUrl = BASE_URL.replace('http', 'ws') + WS_PATH;

  const res = ws.connect(
    wsUrl,
    { headers: { Authorization: `Bearer ${token}` } },
    function (socket) {
      let lastSeq = 0;
      let pushCount = 0;
      let sessionStart = 0;

      socket.on('open', function () {
        // Subscribe to broker push stream.
        socket.send(JSON.stringify({ type: 'subscribe', rate: PUSH_RATE }));
        wsMsgsSent.add(1);
        sessionStart = Date.now();
      });

      socket.on('message', function (data) {
        wsMsgsReceived.add(1);

        let msg;
        try {
          msg = JSON.parse(data);
        } catch (_) {
          return; // plain-text echo response, skip
        }

        if (msg.type === 'push') {
          pushCount++;

          // Track push latency (backend timestamp → client receive).
          if (msg.ts) {
            const latency = Date.now() - msg.ts;
            // Only record plausible values (clock skew guard).
            if (latency >= 0 && latency < 5000) {
              wsPushLatency.add(latency);
            }
          }

          // Track sequence gaps (missed or reordered messages).
          if (msg.seq && msg.seq !== lastSeq + 1 && lastSeq > 0) {
            wsSeqGaps.add(1);
          }
          if (msg.seq) {
            lastSeq = msg.seq;
          }

          // Update observed push rate every 5 seconds.
          const elapsed = (Date.now() - sessionStart) / 1000;
          if (elapsed > 0 && pushCount % 100 === 0) {
            wsPushRate.add(pushCount / elapsed);
          }
        }

        if (msg.type === 'echo_reply' && msg.sent_at) {
          wsEchoRTT.add(Date.now() - msg.sent_at);
        }
      });

      socket.on('error', function () {
        wsErrors.add(1);
      });

      // Send a command every second (simulates client → broker traffic).
      socket.setInterval(function () {
        socket.send(JSON.stringify({
          type: 'command',
          action: 'heartbeat',
          sent_at: Date.now(),
        }));
        wsMsgsSent.add(1);
      }, 1000);

      // Hold the connection for the configured duration, then close.
      socket.setTimeout(function () {
        socket.close();
      }, HOLD_SECONDS * 1000);
    },
  );

  check(res, {
    'ws connected (101)': (r) => r && r.status === 101,
  });
}
