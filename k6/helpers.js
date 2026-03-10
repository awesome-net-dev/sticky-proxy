import crypto from 'k6/crypto';
import encoding from 'k6/encoding';

/**
 * Generate an HS256 JWT compatible with sticky-proxy.
 *
 * @param {string|number} sub  Routing claim value (user identifier).
 * @param {string}        secret  JWT signing secret.
 * @returns {string} Signed JWT token.
 */
export function generateJWT(sub, secret) {
  const header = encoding.b64encode(
    JSON.stringify({ alg: 'HS256', typ: 'JWT' }),
    'rawurl',
  );
  const payload = encoding.b64encode(
    JSON.stringify({
      sub: String(sub),
      exp: Math.floor(Date.now() / 1000) + 86400 * 365,
    }),
    'rawurl',
  );
  const unsigned = `${header}.${payload}`;
  const sig = crypto.hmac('sha256', secret, unsigned, 'base64');
  const signature = sig
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');
  return `${unsigned}.${signature}`;
}

/**
 * Build k6 request params with JWT Authorization header.
 */
export function authParams(token) {
  return { headers: { Authorization: `Bearer ${token}` } };
}
