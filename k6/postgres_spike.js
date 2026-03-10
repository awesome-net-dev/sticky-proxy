// Spike test with PostgreSQL assignment store.
//
// Prerequisites:
//   docker compose -f docker-compose.postgres.yml up -d --build
//   cat k6/seed_postgres.sql | docker compose -f docker-compose.postgres.yml exec -T postgres \
//     psql -U stickyproxy -d stickyproxy
//
// Usage:
//   k6 run k6/postgres_spike.js
//
// What it tests:
//   - PostgreSQL assignment under sudden 10x load increase
//   - Connection pool behavior under pressure
//   - Recovery after spike subsides

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
    spike: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 50 },
        { duration: '1m', target: 50 },
        { duration: '10s', target: 500 },
        { duration: '30s', target: 500 },
        { duration: '10s', target: 50 },
        { duration: '1m', target: 50 },
        { duration: '20s', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<500'],
    http_req_failed: ['rate<0.01'],
  },
};

export default function () {
  const userId = Math.floor(Math.random() * TOTAL_USERS);
  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  check(res, {
    'status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });

  sleep(0.05);
}
