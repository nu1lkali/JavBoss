import { apiFetch, apiError, jsonHeaders } from '@/api/client'
import { zh } from '@/utils/i18n'

export async function fetchDirectories() {
  const res = await apiFetch('/directories', { cache: 'no-store' })
  if (!res.ok) throw await apiError(res)
  const ct = res.headers.get('content-type') || ''
  if (!ct.includes('application/json')) {
    console.warn(
      zh('目录接口返回非 JSON，响应类型:', 'Directory API returned non-JSON content type:'),
      ct
    )
    return []
  }
  return res.json()
}

export async function createDirectory({ path }) {
  const res = await apiFetch('/directories', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify({ path }),
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

export async function browseDirectories(path = '', { showHidden = false, signal } = {}) {
  const params = new URLSearchParams({ path, show_hidden: String(showHidden) })
  const res = await apiFetch(`/directories/browse?${params}`, {
    cache: 'no-store',
    signal,
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

export async function updateDirectory(id, payload) {
  const res = await apiFetch(`/directories/${id}`, {
    method: 'PATCH',
    headers: jsonHeaders,
    body: JSON.stringify(payload),
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

export async function deleteDirectory(id) {
  return updateDirectory(id, { is_delete: true })
}

export async function processDirectory(id, mode, layout = 'prefix') {
  const res = await apiFetch(`/directories/${id}/process`, {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify({ mode, layout }),
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

export async function scanDirectory(id) {
  const res = await apiFetch(`/directories/${id}/scan`, {
    method: 'POST',
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

// 启动一次空壳 NFO 重建任务（后台执行，立即返回）。
// 整库重建可能持续几分钟，调用方需要接着用 fetchNFORebuildStatus() 轮询，
// 直到 status.running 变为 false，再读 status.summary。
// directoryId 传 0 表示所有目录；dryRun 为 true 时只统计数量、不写盘。
export async function rebuildNFO({ directoryId = 0, dryRun = false } = {}) {
  const res = await apiFetch('/nfo/rebuild', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify({ directory_id: directoryId, dry_run: dryRun }),
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

// 查询 NFO 重建任务的进度（running / summary / error）。
export async function fetchNFORebuildStatus() {
  const res = await apiFetch('/nfo/rebuild', { cache: 'no-store' })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}

// 试连 Emby：地址和密钥可以是刚填写、还没保存的值，保存前就能自检。
// 成功时返回 { server_name, version }；失败时抛出带中文原因的 Error。
export async function testEmbyConnection({ url, apiKey }) {
  const res = await apiFetch('/emby/test', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify({ url, api_key: apiKey }),
  })
  if (!res.ok) {
    throw await apiError(res)
  }
  return res.json()
}
