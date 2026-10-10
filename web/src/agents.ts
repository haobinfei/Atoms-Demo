// AI 团队人设：与 server 端流水线的事件 agent 字段对应。
export interface Persona {
  id: string
  name: string
  role: string
  color: string
}

export const PERSONAS: Record<string, Persona> = {
  mike: { id: 'mike', name: 'Mike', role: '团队负责人', color: '#f97316' },
  emma: { id: 'emma', name: 'Emma', role: '产品经理', color: '#ec4899' },
  bob: { id: 'bob', name: 'Bob', role: '架构师', color: '#8b5cf6' },
  alex: { id: 'alex', name: 'Alex', role: '工程师', color: '#3b82f6' },
  sarah: { id: 'sarah', name: 'Sarah', role: 'SEO 专家', color: '#14b8a6' },
  nina: { id: 'nina', name: 'Nina', role: '评审', color: '#eab308' },
}

export function persona(id: string | undefined): Persona {
  return PERSONAS[id ?? ''] ?? { id: id ?? '', name: id ?? '?', role: '', color: '#6b7280' }
}
