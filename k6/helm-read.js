// Read-path load test for helm's HTTP API, through the gateway (real JWT
// validation + rate-limit path, not a shortcut to helm directly).
//
// Covers the endpoints touched during the 2026-07-23 pagination sync + fleet
// capacity pass: helms list, trades/fills/signals/ordersHistory/eventsHistory
// (all newest-first `before` cursor pagination), positions, portfolio.
//
// Usage:
//   k6 run k6/helm-read.js
//   k6 run -e BASE_URL=http://localhost:8080 -e VUS=20 -e DURATION=30s \
//        -e LOGIN_EMAIL=... -e LOGIN_PASSWORD=... k6/helm-read.js
//
// Env vars (all optional, defaults target the local docker-compose stack):
//   BASE_URL       gateway base URL              default http://localhost:8080
//   LOGIN_EMAIL    account to auth as             default from deployment/environments/identity.env (ADMIN_SEED_EMAIL)
//   LOGIN_PASSWORD password for LOGIN_EMAIL       default from deployment/environments/identity.env (ADMIN_SEED_PASSWORD)
//   VUS            virtual users                  default 10
//   DURATION       run length                     default 30s

import http from 'k6/http';
import { check, group, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const LOGIN_EMAIL = __ENV.LOGIN_EMAIL || 'nguyengiapnfif@gmail.com';
const LOGIN_PASSWORD = __ENV.LOGIN_PASSWORD || 'Admin@123456';

const errorRate = new Rate('helm_errors');
const pageLatency = new Trend('helm_page_latency_ms');

export const options = {
  scenarios: {
    read: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 10),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    helm_errors: ['rate<0.01'],
    http_req_duration: ['p(95)<1000'],
  },
};

// setup() runs once, not per-VU: log in and discover one real helm_id so the
// per-VU iterations don't need their own login (JWT is reused, matching how
// a real client behaves) and don't have to guess an id that exists.
export function setup() {
  const loginRes = http.post(
    `${BASE_URL}/api/v1/auth/login`,
    JSON.stringify({ email: LOGIN_EMAIL, password: LOGIN_PASSWORD }),
    { headers: { 'Content-Type': 'application/json' } },
  );
  check(loginRes, { 'login: 200': (r) => r.status === 200 });
  if (loginRes.status !== 200) {
    throw new Error(`login failed: ${loginRes.status} ${loginRes.body}`);
  }
  const token = loginRes.json('data.token.access_token');
  const authHeaders = { headers: { Authorization: `Bearer ${token}` } };

  const helmsRes = http.get(`${BASE_URL}/api/v1/helms`, authHeaders);
  check(helmsRes, { 'list helms: 200': (r) => r.status === 200 });
  const helms = helmsRes.json('data') || [];
  const helmId = helms.length > 0 ? helms[0].id : null;

  return { token, helmId };
}

export default function (data) {
  const { token, helmId } = data;
  const authHeaders = { headers: { Authorization: `Bearer ${token}` } };

  group('business (getMy, unpaginated)', () => {
    const r = http.get(`${BASE_URL}/api/v1/helms`, authHeaders);
    record(r, 'list helms');
  });

  if (!helmId) {
    // No helm seeded for this account — skip the per-helm endpoints rather
    // than fail the whole run. `setup()` already asserted login worked.
    sleep(1);
    return;
  }

  group('helm detail (unpaginated, bounded by nature)', () => {
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/portfolio`, authHeaders), 'portfolio');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/positions`, authHeaders), 'positions');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/orders`, authHeaders), 'orders (live)');
  });

  group('history (before-cursor paginated)', () => {
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/trades?limit=50`, authHeaders), 'trades');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/fills?limit=50`, authHeaders), 'fills');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/signals?limit=50`, authHeaders), 'signals');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/orders/history?limit=50`, authHeaders), 'orders history');
    record(http.get(`${BASE_URL}/api/v1/helms/${helmId}/events/history?limit=50`, authHeaders), 'events history');
  });

  sleep(1);
}

function record(res, label) {
  const ok = check(res, {
    [`${label}: status 200`]: (r) => r.status === 200,
  });
  errorRate.add(!ok);
  pageLatency.add(res.timings.duration, { endpoint: label });
}
