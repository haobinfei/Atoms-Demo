# Browser E2E

Requires Docker Compose, Go, Node.js and Playwright Chromium. From the repo root:

```sh
docker compose -f e2e/compose.yaml up -d --wait
cd e2e
npm ci
npm run install:browsers
npm test
```

Playwright starts an E2E-only Go API on port 4173, using the real HTTP, identity, Postgres and SMTP implementations with a deterministic template provider, plus a Vite preview on port 5199. The test-only Postgres and Mailpit bind to `127.0.0.1:55432`, `:51025` and `:58025`. The database is initialized from the local baseline and identity schema; its container data is discarded when the Compose stack is stopped. Verification links are read through the Mailpit API, so no external mailbox or SMTP credentials are used.

The desktop and mobile suites cover registration and verification, login and logout, invitations and role changes, interactive generation, anonymous published-app access and its revocation. Go identity integration tests also use this Postgres instance when `IDENTITY_TEST_DSN` is set to the local E2E DSN shown in the root README; otherwise those tests skip.

When finished, run `docker compose -f e2e/compose.yaml down`. Ports must be free before starting the stack.
