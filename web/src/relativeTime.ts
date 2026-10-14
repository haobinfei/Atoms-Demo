// relativeTime formats a date as a human-friendly relative string.
// Examples: "刚刚", "2 分钟前", "3 小时前", "昨天", "5 天前", "2 个月前".
export function relativeTime(date: Date | string): string {
  const now = Date.now()
  const then = typeof date === 'string' ? new Date(date).getTime() : date.getTime()
  const seconds = Math.floor((now - then) / 1000)

  if (seconds < 10) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`

  const days = Math.floor(hours / 24)
  if (days === 1) return '昨天'
  if (days < 30) return `${days} 天前`

  const months = Math.floor(days / 30)
  if (months < 12) return `${months} 个月前`

  const years = Math.floor(months / 12)
  return `${years} 年前`
}
