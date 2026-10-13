import { type Page } from '@playwright/test'

interface StubOptions {
  status: string
  previewReady: boolean
  events: Record<string, unknown>[]
}

export interface SessionStub {
  // Lets a case move the session out of 构建中 so a follow-up queued behind it
  // can be seen going out.
  setStatus(status: string): void
}

// stubSession drives the client with an explicit single-session payload and
// an empty event stream.
//
// Two reasons it is built this way:
//   - the deterministic template provider always builds successfully, so the
//     failed and no-build branches cannot be reached by timing;
//   - the payload is constructed rather than relayed from the real response,
//     because a response fetched inside a route handler is disposed when the
//     page reloads, which made the relayed version fail intermittently.
//
// The server contract behind these branches is covered by the HTTP-layer
// tests; what is under test here is the client wiring.
export async function stubSession(page: Page, opts: StubOptions): Promise<SessionStub> {
  let status = opts.status
  // An empty stream: EventSource retries, but no 完成 event ever arrives to
  // overwrite the stubbed status.
  await page.route(/\/api\/sessions\/[^/]+\/events/, async (route) => {
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
  })
  await page.route(/\/api\/sessions\/[^/]+$/, async (route) => {
    const url = route.request().url()
    if (url.endsWith('/list') || route.request().method() !== 'GET') {
      await route.fallback()
      return
    }
    const id = url.split('?')[0].split('/').pop() ?? ''
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        id,
        prompt: '一家咖啡店的电商网站',
        status,
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        messages: [],
        events: opts.events,
        previewReady: opts.previewReady,
      }),
    })
  })
  return {
    setStatus: (next: string) => {
      status = next
    },
  }
}
