import { API } from '../helpers/api';
import { isAdmin } from '../helpers/utils';

/**
 * 用量事实层服务封装
 * 严格遵循后端口径：
 * - 管理员：GET /api/usage/summary, /trend, /by-model, /by-user, /by-channel
 * - 普通用户：GET /api/usage/self/summary, /self/trend
 * - 不在前端推导费用、倍率或命中率，可空指标保留 null
 */

export function serializeUsageFilters(filters = {}) {
  const params = new URLSearchParams();
  if (filters.start_timestamp) params.append('start_timestamp', filters.start_timestamp);
  if (filters.end_timestamp) params.append('end_timestamp', filters.end_timestamp);
  if (filters.user_id && isAdmin()) params.append('user_id', filters.user_id);
  if (filters.token_id) params.append('token_id', filters.token_id);
  if (filters.channel_id && isAdmin()) params.append('channel_id', filters.channel_id);
  if (filters.model_name) params.append('model_name', filters.model_name);
  if (filters.limit) params.append('limit', filters.limit);
  return params;
}

export function parseUsageFilters(searchParams) {
  const sp = searchParams instanceof URLSearchParams ? searchParams : new URLSearchParams(searchParams || '');
  const filters = {};
  if (sp.get('start_timestamp')) filters.start_timestamp = parseInt(sp.get('start_timestamp'), 10);
  if (sp.get('end_timestamp')) filters.end_timestamp = parseInt(sp.get('end_timestamp'), 10);
  if (sp.get('user_id')) filters.user_id = sp.get('user_id');
  if (sp.get('token_id')) filters.token_id = sp.get('token_id');
  if (sp.get('channel_id')) filters.channel_id = sp.get('channel_id');
  if (sp.get('model_name')) filters.model_name = sp.get('model_name');
  if (sp.get('limit')) filters.limit = parseInt(sp.get('limit'), 10);
  return filters;
}

export async function getUsageSummary(filters = {}, scopeOverride = null) {
  const isAdm = scopeOverride === 'admin' ? true : scopeOverride === 'self' ? false : isAdmin();
  const endpoint = isAdm ? '/api/usage/summary' : '/api/usage/self/summary';
  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  return res.data;
}

export async function getUsageTrend(filters = {}, scopeOverride = null) {
  const isAdm = scopeOverride === 'admin' ? true : scopeOverride === 'self' ? false : isAdmin();
  const endpoint = isAdm ? '/api/usage/trend' : '/api/usage/self/trend';
  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  return res.data;
}

export async function getUsageRanking(dimension = 'model', filters = {}) {
  if (!isAdmin()) {
    throw new Error('Ranking endpoints are restricted to administrators');
  }
  let endpoint = '/api/usage/by-model';
  if (dimension === 'user') endpoint = '/api/usage/by-user';
  if (dimension === 'channel') endpoint = '/api/usage/by-channel';

  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  return res.data;
}
