// Steady-state test with PostgreSQL assignment store.
//
// Prerequisites:
//   docker compose -f docker-compose.postgres.yml up -d --build
//   cat k6/seed_postgres.sql | docker compose -f docker-compose.postgres.yml exec -T postgres \
//     psql -U stickyproxy -d stickyproxy
//
// Usage:
//   k6 run k6/postgres_steady.js
//
// What it tests:
//   - Assignment-mode routing with PostgreSQL backend
//   - Least-loaded assignment for new users
//   - Local cache hit rate after warm-up
//   - Even distribution across backends via weighted assignment

import http from 'k6/http';
import { check, sleep } from 'k6';
import { SharedArray } from 'k6/data';
import { generateJWT, authParams } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '200');

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
        { duration: '30s', target: 50 },
        { duration: '1m', target: 100 },
        { duration: '5m', target: 100 },
        { duration: '30s', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<100', 'p(99)<200'],
    http_req_failed: ['rate<0.001'],
  },
};

export default function () {
  const userId = (__VU - 1) % TOTAL_USERS;
  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  check(res, {
    'status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });

  sleep(0.1);
}
