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
  // Replaces the persisted 事件流 the session payload reports.
  setEvents(events: Record<string, unknown>[]): void
  // Delivers these events on the next (re)connect of the 事件流, which is how
  // a 回合 ends without a real server: the client only learns about a 取消回合
  // (Cancel Turn) from the stream. Later reconnects replay the same seqs,
  // which the client dedupes.
  setStream(events: Record<string, unknown>[]): void
}

// stubSession drives the client with an explicit single-session payload and a
// controllable event stream.
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
  let events = opts.events
  let stream: Record<string, unknown>[] = []
  await page.route(/\/api\/sessions\/[^/]+\/events/, async (route) => {
    const body = stream.map((ev) => `data: ${JSON.stringify(ev)}\n\n`).join('')
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body })
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
        events,
        previewReady: opts.previewReady,
      }),
    })
  })
  return {
    setStatus: (next: string) => {
      status = next
    },
    setEvents: (next: Record<string, unknown>[]) => {
      events = next
    },
    setStream: (next: Record<string, unknown>[]) => {
      stream = next
    },
  }
}
