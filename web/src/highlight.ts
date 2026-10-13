// A deliberately small source highlighter.
//
// It is a single sequential scan, not a parser: it knows comments, strings,
// numbers and keywords, and it is right about the one thing a naive regex
// gets wrong — a `//` inside a string is not a comment, because the scanner
// decides what it is looking at one character at a time.
//
// Anything it does not recognise stays plain, so the worst case is "less
// colour", never "wrong text".

export type TokenKind = 'plain' | 'comment' | 'string' | 'number' | 'keyword'

export interface Token {
  text: string
  kind: TokenKind
}

interface Syntax {
  line?: string
  block?: [open: string, close: string]
  keywords: ReadonlySet<string>
}

const JS_KEYWORDS = new Set([
  'as', 'async', 'await', 'break', 'case', 'catch', 'class', 'const', 'continue',
  'default', 'delete', 'do', 'else', 'enum', 'export', 'extends', 'false',
  'finally', 'for', 'from', 'function', 'if', 'implements', 'import', 'in',
  'instanceof', 'interface', 'let', 'new', 'null', 'of', 'return', 'satisfies',
  'static', 'super', 'switch', 'this', 'throw', 'true', 'try', 'type', 'typeof',
  'undefined', 'var', 'void', 'while', 'yield',
])

const NONE: ReadonlySet<string> = new Set()

// Per-extension: which comment forms exist, and which words are keywords. A
// language we do not know still gets comments, strings and numbers.
function syntaxFor(path: string): Syntax {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase()
  switch (ext) {
    case 'ts':
    case 'tsx':
    case 'js':
    case 'jsx':
      return { line: '//', block: ['/*', '*/'], keywords: JS_KEYWORDS }
    case 'css':
      return { block: ['/*', '*/'], keywords: NONE }
    case 'html':
    case 'htm':
      return { block: ['<!--', '-->'], keywords: NONE }
    case 'json':
      return { keywords: NONE }
    case 'md':
      return { keywords: NONE }
    default:
      return { line: '//', block: ['/*', '*/'], keywords: NONE }
  }
}

function isIdentStart(c: string): boolean {
  return /[A-Za-z_$]/.test(c)
}

function isIdentPart(c: string): boolean {
  return /[A-Za-z0-9_$]/.test(c)
}

function isDigit(c: string): boolean {
  return c >= '0' && c <= '9'
}

// scanString returns the end index (exclusive) of the string literal starting
// at i, honouring backslash escapes. An unterminated literal runs to the end,
// which keeps the output lossless.
function scanString(source: string, i: number, quote: string): number {
  let j = i + 1
  while (j < source.length) {
    const c = source[j]
    if (c === '\\') {
      j += 2
      continue
    }
    if (c === quote) return j + 1
    j += 1
  }
  return source.length
}

export function tokenize(source: string, path: string): Token[] {
  const syntax = syntaxFor(path)
  const tokens: Token[] = []
  let plain = ''
  const flush = () => {
    if (plain) {
      tokens.push({ text: plain, kind: 'plain' })
      plain = ''
    }
  }
  let i = 0
  while (i < source.length) {
    const c = source[i]

    if (syntax.line && source.startsWith(syntax.line, i)) {
      flush()
      const end = source.indexOf('\n', i)
      const stop = end === -1 ? source.length : end
      tokens.push({ text: source.slice(i, stop), kind: 'comment' })
      i = stop
      continue
    }
    if (syntax.block && source.startsWith(syntax.block[0], i)) {
      flush()
      const end = source.indexOf(syntax.block[1], i + syntax.block[0].length)
      const stop = end === -1 ? source.length : end + syntax.block[1].length
      tokens.push({ text: source.slice(i, stop), kind: 'comment' })
      i = stop
      continue
    }
    if (c === '"' || c === "'" || c === '`') {
      flush()
      const stop = scanString(source, i, c)
      tokens.push({ text: source.slice(i, stop), kind: 'string' })
      i = stop
      continue
    }
    if (isDigit(c) && !isIdentPart(source[i - 1] ?? '')) {
      flush()
      let j = i
      while (j < source.length && /[0-9a-fA-FxX._]/.test(source[j])) j += 1
      tokens.push({ text: source.slice(i, j), kind: 'number' })
      i = j
      continue
    }
    if (isIdentStart(c)) {
      let j = i
      while (j < source.length && isIdentPart(source[j])) j += 1
      const word = source.slice(i, j)
      if (syntax.keywords.has(word)) {
        flush()
        tokens.push({ text: word, kind: 'keyword' })
      } else {
        plain += word
      }
      i = j
      continue
    }
    plain += c
    i += 1
  }
  flush()
  return tokens
}

// splitLines turns tokens into one token list per source line. A token may
// carry newlines of its own (a block comment, a template literal), so the
// split happens here rather than by rendering each line separately — which is
// what keeps line numbers aligned with the code.
export function splitLines(tokens: Token[]): Token[][] {
  const lines: Token[][] = [[]]
  for (const token of tokens) {
    const parts = token.text.split('\n')
    parts.forEach((part, index) => {
      if (index > 0) lines.push([])
      if (part) lines[lines.length - 1].push({ text: part, kind: token.kind })
    })
  }
  return lines
}

// highlight is the one call the drawer needs: source in, lines of tokens out.
export function highlight(source: string, path: string): Token[][] {
  return splitLines(tokenize(source, path))
}
