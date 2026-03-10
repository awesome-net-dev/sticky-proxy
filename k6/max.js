// Maximum throughput test — pushes the proxy to CPU saturation on 4 cores.
//
// Prerequisites:
//   docker compose -f docker-compose.postgres.yml up -d --build
//   (set proxy cpus: "4" in docker-compose.postgres.yml)
//   cat k6/seed_postgres.sql | docker compose -f docker-compose.postgres.yml exec -T postgres \
//     psql -U stickyproxy -d stickyproxy
//
// Usage:
//   k6 run k6/max.js
//
// What it tests:
//   - Maximum sustained throughput on 4 CPU cores
//   - Behavior under full CPU saturation
//   - Latency degradation curve as load increases

import http from 'k6/http';
import { check } from 'k6';
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
    max_throughput: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 200 },   // warm up
        { duration: '30s', target: 500 },   // ramp
        { duration: '30s', target: 1000 },  // push harder
        { duration: '1m', target: 1000 },   // sustain peak
        { duration: '30s', target: 1500 },  // overshoot
        { duration: '1m', target: 1500 },   // sustain max
        { duration: '30s', target: 0 },     // cool down
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(99)<1000'],
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

  // No sleep — fire as fast as possible to find the ceiling.
}
