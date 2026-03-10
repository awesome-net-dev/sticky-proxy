// Soak test — detects memory leaks, connection leaks, and latency degradation.
//
// Usage:
//   k6 run k6/soak.js
//   k6 run -e DURATION=2h -e TOTAL_USERS=50000 k6/soak.js
//
// What it tests:
//   - Gentle, sustained load over 1 hour (default)
//   - 80/20 traffic mix: 80% hot users (cache hits), 20% random (cache misses)
//   - Stable p99 latency over time (no creep)
//
// Acceptance criteria:
//   - p99 latency  < 100ms sustained
//   - Error rate    < 0.01%
//
// Monitor alongside:
//   watch -n5 'curl -s http://localhost:8080/metrics | grep -E "active_conn|healthy_back"'

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { generateJWT, authParams } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '10000');
const DURATION = __ENV.DURATION || '1h';

const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

export const options = {
  scenarios: {
    soak: {
      executor: 'constant-vus',
      vus: 20,
      duration: DURATION,
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<100'],
    http_req_failed: ['rate<0.0001'],
  },
};

export default function () {
  // 80% hot path (same user per VU = cache hit), 20% cold path (random user).
  let userId;
  if (Math.random() < 0.8) {
    userId = (__VU - 1) % TOTAL_USERS;
  } else {
    userId = Math.floor(Math.random() * TOTAL_USERS);
  }

  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  check(res, {
    'status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });

  sleep(0.5); // ~2 req/s per VU → ~40 RPS (gentle for long-running test)
}
