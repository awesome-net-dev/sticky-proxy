// Steady-state load test — finds the sustainable throughput ceiling.
//
// Usage:
//   k6 run k6/steady_state.js
//   k6 run -e PROXY_URL=http://host:8080 -e TOTAL_USERS=5000 k6/steady_state.js
//
// What it tests:
//   - Sustained 1000 RPS with 100 concurrent users over 5 minutes
//   - Sticky routing (each VU reuses the same user across iterations)
//   - Local cache hit rate (most requests should hit the in-memory cache)
//
// Acceptance criteria:
//   - p95 latency  < 50ms
//   - p99 latency  < 100ms
//   - Error rate    < 0.1%

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { generateJWT, authParams } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '1000');

// Pre-generate all JWT tokens once; shared across VUs (memory-efficient).
const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

export const options = {
  scenarios: {
    steady: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 50 },  // warm up
        { duration: '1m', target: 100 },  // ramp to target
        { duration: '5m', target: 100 },  // hold steady
        { duration: '30s', target: 0 },   // ramp down
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<50', 'p(99)<100'],
    http_req_failed: ['rate<0.001'],
  },
};

export default function () {
  // Each VU maps to one user — tests sticky routing over repeated requests.
  const userId = (__VU - 1) % TOTAL_USERS;
  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  check(res, {
    'status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });

  sleep(0.1); // ~10 req/s per VU → ~1000 RPS at 100 VUs
}
