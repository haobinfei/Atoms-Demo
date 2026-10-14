import { putThumbnail } from './api'

// Capturing the preview frame into the dashboard's cover image (ticket #56).
//
// The preview iframe is same-origin, so its rendered DOM can be read: the
// document is cloned into an SVG foreignObject, drawn onto a canvas, and
// exported as a PNG. All of it runs in the browser — the server has no
// headless browser by decision (docs/design §8.1 依赖政策), and the capture
// costs nothing until a frame is actually on screen.
//
// Every failure path resolves to null instead of throwing: a cover that
// could not be captured is a missing cover, not a broken page. The next
// frame load tries again.

// A capture larger than the preview pane would only inflate the upload; the
// dashboard renders the image at ~148px tall anyway.
const MAX_EDGE = 1280

// onLoad fires when the document is ready, not when the generated app has
// painted its first frame. React mounts right after; without this pause the
// capture is a blank page with the right styles.
const PAINT_SETTLE_MS = 300

// cloneDocument serializes the frame's document for the foreignObject.
//
// Two details matter:
// - <script> tags are dropped. They cannot run inside an SVG image, and the
//   bundle is an external file that would not load there either; what we
//   need is the DOM React already rendered plus the <style> the bundle
//   injected at runtime — both survive the clone.
// - <link> stylesheets stay in the clone but do not load in image mode
//   (external resources are blocked). Generated apps carry their CSS inline,
//   so this only affects hand-written apps that reach the preview.
function cloneDocument(doc: Document): string {
  const root = doc.documentElement.cloneNode(true) as HTMLElement
  root.querySelectorAll('script').forEach((node) => node.remove())
  return new XMLSerializer().serializeToString(root)
}

// captureThumbnail renders the frame's current document to a PNG blob.
// Returns null when the frame is not readable, the render fails, or the
// canvas comes back tainted.
export async function captureThumbnail(frame: HTMLIFrameElement): Promise<Blob | null> {
  const doc = frame.contentDocument
  if (!doc?.documentElement || !doc.body) return null
  await new Promise((resolve) => setTimeout(resolve, PAINT_SETTLE_MS))

  const width = Math.max(1, Math.min(frame.clientWidth || 640, MAX_EDGE))
  const height = Math.max(1, Math.min(frame.clientHeight || 400, MAX_EDGE))
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}">` +
    `<foreignObject x="0" y="0" width="100%" height="100%">${cloneDocument(doc)}</foreignObject>` +
    '</svg>'

  try {
    const image = new Image()
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve()
      image.onerror = () => reject(new Error('svg image did not load'))
      image.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg)
    })
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const ctx = canvas.getContext('2d')
    if (!ctx) return null
    ctx.drawImage(image, 0, 0, width, height)
    return await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'))
  } catch {
    return null
  }
}

// captureAndUpload is the one call the workspace makes: capture the frame,
// and if there is anything to send, send it. A failed capture is silent —
// the dashboard falls back to its placeholder until the next attempt.
export async function captureAndUpload(sessionId: string, frame: HTMLIFrameElement): Promise<void> {
  const blob = await captureThumbnail(frame)
  if (!blob || blob.size === 0) return
  await putThumbnail(sessionId, blob)
}
