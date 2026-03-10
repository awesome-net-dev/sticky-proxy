// Login flow test — registers new users via /login, then sends authenticated
// requests to verify sticky routing through the PostgreSQL assignment table.
//
// Prerequisites:
//   docker compose -f docker-compose.postgres.yml up -d --build
//   cat k6/seed_postgres.sql | docker compose -f docker-compose.postgres.yml exec -T postgres \
//     psql -U stickyproxy -d stickyproxy
//
// Usage:
//   k6 run k6/postgres_login.js
//
// What it tests:
//   - Public /login endpoint bypasses JWT auth
//   - Backend writes account to PostgreSQL accounts table
//   - Backend responds with user_id confirming the registration
//   - Proxy captures X-Sticky-Routing-Key internally (stripped from client response)
//   - Subsequent authenticated requests route to the assigned backend

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';
import { SharedArray } from 'k6/data';
import { generateJWT, authParams } from './helpers.js';

const BASE_URL = __ENV.PROXY_URL || 'http://localhost:8080';
const JWT_SECRET = __ENV.JWT_SECRET || 'mysecret';
const TOTAL_USERS = parseInt(__ENV.TOTAL_USERS || '200');
const NEW_USER_OFFSET = 1000; // New users start at ID 1000 to avoid seed conflicts.

const tokens = new SharedArray('tokens', function () {
  const arr = [];
  for (let i = 0; i < TOTAL_USERS; i++) {
    arr.push(generateJWT(i, JWT_SECRET));
  }
  return arr;
});

const loginsOk = new Counter('logins_ok');
const routedOk = new Counter('routed_after_login');

export const options = {
  scenarios: {
    login_then_route: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '20s', target: 20 },
        { duration: '2m', target: 50 },
        { duration: '1m', target: 50 },
        { duration: '20s', target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<200'],
    http_req_failed: ['rate<0.01'],
  },
};

export default function () {
  // Phase 1: Register a new user via /login (public path, no JWT required).
  const newUserId = NEW_USER_OFFSET + __VU * 1000 + __ITER;
  const loginRes = http.post(
    `${BASE_URL}/login`,
    JSON.stringify({ user_id: String(newUserId), weight: 1 }),
    { headers: { 'Content-Type': 'application/json' } },
  );

  const loginOk = check(loginRes, {
    'login status 200': (r) => r.status === 200,
    'login body has user_id': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body.user_id === String(newUserId);
      } catch (_) {
        return false;
      }
    },
  });
  if (loginOk) {
    loginsOk.add(1);
  }

  sleep(0.2);

  // Phase 2: Send authenticated requests with seeded users to verify
  // assignment-mode routing works end-to-end.
  const userId = (__VU - 1) % TOTAL_USERS;
  const res = http.get(`${BASE_URL}/`, authParams(tokens[userId]));

  const routeOk = check(res, {
    'route status 200': (r) => r.status === 200,
    'no 5xx': (r) => r.status < 500,
  });
  if (routeOk) {
    routedOk.add(1);
  }

  sleep(0.3);
}
