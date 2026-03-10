// Spike test — measures resilience under sudden traffic bursts.
//
// Usage:
//   k6 run k6/spike.js
//   k6 run -e PROXY_URL=http://host:8080 -e TOTAL_USERS=10000 k6/spike.js
//
// What it tests:
//   - 10x traffic spike (50 → 500 VUs) sustained for 30 seconds
//   - Many unique users hitting the proxy (random selection = cache misses)
//   - Recovery behavior after spike subsides
//
// Acceptance criteria:
//   - p99 latency  < 200ms (including during spike)
//   - Error rate    < 0.5%

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { generateJWT, authParams } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '5000');

const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

export const options = {
  scenarios: {
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 50 },   // warm up
        { duration: '1m', target: 50 },    // baseline
        { duration: '10s', target: 500 },  // SPIKE — 10x jump
        { duration: '30s', target: 500 },  // hold spike
        { duration: '10s', target: 50 },   // drop back
        { duration: '1m', target: 50 },    // verify recovery
        { duration: '20s', target: 0 },    // ramp down
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<200'],
    http_req_failed: ['rate<0.005'],
  },
};

export default function () {
  // Random user selection — maximizes cache misses during spike.
  const userId = Math.floor(Math.random() * TOTAL_USERS);
  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  check(res, {
    'status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });

  sleep(0.05); // ~20 req/s per VU
}
