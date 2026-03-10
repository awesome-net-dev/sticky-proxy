// WebSocket concurrency test — measures sustained connection capacity.
//
// Usage:
//   k6 run k6/websocket.js
//   k6 run -e WS_PATH=/ws -e WS_HOLD=60 -e TOTAL_USERS=1000 k6/websocket.js
//
// What it tests:
//   - Ramp to 200 concurrent WebSocket connections
//   - Each connection held open for WS_HOLD seconds (default 30)
//   - Periodic ping messages to keep connections alive
//   - Bidirectional relay through the proxy
//
// Acceptance criteria:
//   - All connections upgrade successfully (status 101)
//   - < 10 WebSocket errors total

import ws from 'k6/ws';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { generateJWT } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '500');
const WS_PATH = __ENV.WS_PATH || '/ws';
const HOLD_SECONDS = parseInt(__ENV.WS_HOLD || '30');

const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

const wsErrors = new Counter('ws_errors');
const wsMessages = new Counter('ws_messages_sent');

export const options = {
  scenarios: {
    websockets: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 50 },   // warm up
        { duration: '1m', target: 200 },   // ramp to 200 connections
        { duration: '2m', target: 200 },   // hold 200 concurrent WS
        { duration: '30s', target: 0 },    // ramp down
      ],
    },
  },
  thresholds: {
    ws_errors: ['count<10'],
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
      socket.on('open', function () {
        socket.send(JSON.stringify({ type: 'ping', user: userId }));
        wsMessages.add(1);
      });

      socket.on('message', function () {
        wsMessages.add(1);
      });

      socket.on('error', function () {
        wsErrors.add(1);
      });

      // Send a ping every 5 seconds to keep the connection alive.
      socket.setInterval(function () {
        socket.send(JSON.stringify({ type: 'ping', ts: Date.now() }));
        wsMessages.add(1);
      }, 5000);

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
