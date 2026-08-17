// Mints the HMAC session JWT the local web gateway accepts in place of a Clerk session.
//
// The gateway only honours these when started with WEB_GATEWAY_TOKEN_VERIFIER=hmac, which
// it refuses to do if CLERK_SECRET_KEY is set — so this can never be a path into a real
// environment, only into a stack deliberately booted for testing.
//
// Usage: node scripts/local-jwt.mjs [sub-id]
import { createHmac } from 'node:crypto';

// Must match the harness defaults the fullstack entrypoint uses.
const SECRET = process.env.MIRADOR_LOCAL_HMAC_SECRET
  ?? 'test-jwt-secret-for-integration-tests';
const ISSUER = 'mirador-integration-tests';
const AUDIENCE = 'mirador-web-gateway';

const sub = process.argv[2] || process.env.MIRADOR_LOCAL_SUB_ID || 'local-cli-test-user';
const now = Math.floor(Date.now() / 1000);

const b64 = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
const header = b64({ alg: 'HS256', typ: 'JWT' });
const payload = b64({ sub, iss: ISSUER, aud: AUDIENCE, iat: now, exp: now + 3600 });
const signature = createHmac('sha256', SECRET)
  .update(`${header}.${payload}`)
  .digest('base64url');

process.stdout.write(`${header}.${payload}.${signature}`);
