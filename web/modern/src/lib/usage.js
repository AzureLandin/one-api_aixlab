import { API, isAdmin } from '../lib/api';

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

export async function getUsageSummary(filters = {}) {
  const endpoint = isAdmin() ? '/api/usage/summary' : '/api/usage/self/summary';
  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  return res.data;
}

export async function getUsageTrend(filters = {}) {
  const endpoint = isAdmin() ? '/api/usage/trend' : '/api/usage/self/trend';
  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  if (res.data?.success && Array.isArray(res.data.data)) {
    // 拍平每日趋势数据，供 Recharts 图表直接读取
    const flattened = res.data.data.map((row) => ({
      date: row.day,
      day: row.day,
      total_tokens: row.summary?.total_tokens || 0,
      input_tokens: row.summary?.input_tokens || 0,
      output_tokens: row.summary?.output_tokens || 0,
      cache_read_tokens: row.summary?.cache_read_input_tokens || 0,
      request_count: row.summary?.request_count || 0,
    }));
    return { ...res.data, data: flattened };
  }
  return res.data;
}

export async function getUsageRanking(dimension = 'model', filters = {}) {
  if (!isAdmin()) {
    throw new Error('Ranking endpoints require administrator role');
  }
  let endpoint = '/api/usage/by-model';
  if (dimension === 'user') endpoint = '/api/usage/by-user';
  if (dimension === 'channel') endpoint = '/api/usage/by-channel';

  const params = serializeUsageFilters(filters);
  const res = await API.get(endpoint, { params });
  if (res.data?.success && Array.isArray(res.data.data)) {
    // 拍平排行榜数据，供表格及柱状图读取
    const flattened = res.data.data.map((row) => ({
      model_name: row.model_name,
      user_id: row.user_id,
      username: row.username,
      channel_id: row.channel_id,
      channel_name: row.channel_name,
      total_tokens: row.summary?.total_tokens || 0,
      input_tokens: row.summary?.input_tokens || 0,
      output_tokens: row.summary?.output_tokens || 0,
      cache_hit_rate: row.summary?.cache_hit_rate,
      request_count: row.summary?.request_count || 0,
    }));
    return { ...res.data, data: flattened };
  }
  return res.data;
}
