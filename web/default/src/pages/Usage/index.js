import React, { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Grid, Tab, Table, Label, Input } from 'semantic-ui-react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { isAdmin } from '../../helpers';
import { renderNumber } from '../../helpers/render';
import {
  getUsageSummary,
  getUsageTrend,
  getUsageRanking,
  parseUsageFilters,
} from '../../services/usage';

// 共享统计组件
import PageHeader from '../../components/layout/PageHeader';
import MetricCard from '../../components/analytics/MetricCard';
import RateMetric from '../../components/analytics/RateMetric';
import ChartPanel from '../../components/analytics/ChartPanel';
import FilterBar from '../../components/analytics/FilterBar';
import DataQualityNotice from '../../components/analytics/DataQualityNotice';
import PageState from '../../components/states/PageState';

export default function Usage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const admin = isAdmin();

  // 从 URL 深链接恢复状态
  const initialFilters = parseUsageFilters(searchParams);
  const [userIdFilter, setUserIdFilter] = useState(initialFilters.user_id || '');
  const [days, setDays] = useState(
    initialFilters.start_timestamp ? 0 : 7
  );

  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  const [denied, setDenied] = useState(false);

  const [summary, setSummary] = useState(null);
  const [trend, setTrend] = useState([]);
  const [userRank, setUserRank] = useState([]);
  const [modelRank, setModelRank] = useState([]);
  const [channelRank, setChannelRank] = useState([]);

  // 计算时间戳并同步至 URL
  const computeFilters = useCallback(() => {
    const filters = {};
    if (days !== 0) {
      const end = Math.floor(Date.now() / 1000);
      const start = end - days * 86400;
      filters.start_timestamp = start;
      filters.end_timestamp = end;
    }
    // 管理员才允许传 user_id 筛选（防止越权）
    if (admin && userIdFilter.trim()) {
      filters.user_id = userIdFilter.trim();
    }
    return filters;
  }, [days, admin, userIdFilter]);

  const syncURL = useCallback((filters) => {
    const nextParams = new URLSearchParams();
    if (filters.start_timestamp) nextParams.set('start_timestamp', filters.start_timestamp);
    if (filters.end_timestamp) nextParams.set('end_timestamp', filters.end_timestamp);
    if (filters.user_id) nextParams.set('user_id', filters.user_id);
    setSearchParams(nextParams, { replace: true });
  }, [setSearchParams]);

  const loadData = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    else setLoading(true);
    setError(false);
    setDenied(false);

    const filters = computeFilters();
    syncURL(filters);

    try {
      const [sumRes, trendRes] = await Promise.all([
        getUsageSummary(filters),
        getUsageTrend(filters),
      ]);

      if (sumRes.success) setSummary(sumRes.data);
      if (trendRes.success) setTrend(trendRes.data || []);

      if (admin) {
        const [mRes, uRes, cRes] = await Promise.all([
          getUsageRanking('model', { ...filters, limit: 10 }),
          getUsageRanking('user', { ...filters, limit: 10 }),
          getUsageRanking('channel', { ...filters, limit: 10 }),
        ]);
        if (mRes.success) setModelRank(mRes.data || []);
        if (uRes.success) setUserRank(uRes.data || []);
        if (cRes.success) setChannelRank(cRes.data || []);
      }
    } catch (e) {
      const status = e?.response?.status;
      if (status === 401 || status === 403) setDenied(true);
      else setError(true);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [computeFilters, syncURL, admin]);

  useEffect(() => {
    loadData();
  }, [days, userIdFilter]); // 切换天数或用户时自动请求

  if (loading && !summary) {
    return <PageState type="loading" title="正在分析用量事实层数据..." />;
  }

  if (denied) {
    return <PageState type="forbidden" title="无权访问" message="您当前无权查看该用量统计" />;
  }

  if (error && !summary) {
    return (
      <PageState
        type="error"
        title="用量分析加载失败"
        message="无法连接至用量事实层，请检查后重试"
        onRetry={() => loadData(false)}
      />
    );
  }

  const renderRankingTable = (items, keyCol, nameCol) => (
    <div className="table-scroll">
      <Table basic="very" inverted compact size="small">
        <Table.Header>
          <Table.Row>
            <Table.HeaderCell>{nameCol}</Table.HeaderCell>
            <Table.HeaderCell textAlign="right">输入 Token</Table.HeaderCell>
            <Table.HeaderCell textAlign="right">输出 Token</Table.HeaderCell>
            <Table.HeaderCell textAlign="right">总 Token</Table.HeaderCell>
            <Table.HeaderCell textAlign="right">缓存命中率</Table.HeaderCell>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {items.map((it, idx) => (
            <Table.Row key={it[keyCol] || idx}>
              <Table.Cell>
                <span style={{ fontFamily: 'var(--app-font-mono)' }}>{it[keyCol] || `ID: ${it.user_id || it.channel_id}`}</span>
              </Table.Cell>
              <Table.Cell textAlign="right">{renderNumber(it.input_tokens)}</Table.Cell>
              <Table.Cell textAlign="right">{renderNumber(it.output_tokens)}</Table.Cell>
              <Table.Cell textAlign="right">{renderNumber(it.total_tokens)}</Table.Cell>
              <Table.Cell textAlign="right">
                <Label size="tiny" basic color={it.cache_hit_rate > 0 ? 'green' : 'grey'}>
                  {it.cache_hit_rate !== null ? `${(it.cache_hit_rate * 100).toFixed(1)}%` : '—'}
                </Label>
              </Table.Cell>
            </Table.Row>
          ))}
        </Table.Body>
      </Table>
    </div>
  );

  const panes = [
    {
      menuItem: '模型消耗排行',
      render: () => <Tab.Pane basic attached={false}>{renderRankingTable(modelRank, 'model_name', '模型名称')}</Tab.Pane>,
    },
    {
      menuItem: '用户消耗排行',
      render: () => <Tab.Pane basic attached={false}>{renderRankingTable(userRank, 'username', '用户名 / ID')}</Tab.Pane>,
    },
    {
      menuItem: '渠道消耗排行',
      render: () => <Tab.Pane basic attached={false}>{renderRankingTable(channelRank, 'channel_name', '渠道名称 / ID')}</Tab.Pane>,
    },
  ];

  return (
    <div className="usage-page-container">
      <PageHeader
        title="用量分析"
        description="支持多时间跨度过滤、深链接直达与模型/用户/渠道三维度细分排行"
      />

      <FilterBar
        days={days}
        onChangeDays={setDays}
        onRefresh={() => loadData(true)}
        loading={refreshing}
        extra={
          admin ? (
            <Input
              size="small"
              icon="search"
              placeholder="按 User ID 过滤"
              value={userIdFilter}
              onChange={(e) => setUserIdFilter(e.target.value)}
              style={{ width: '160px' }}
            />
          ) : null
        }
      />

      {summary && (
        <>
          {/* 指标卡片矩阵 */}
          <div className="metric-cards-grid">
            <MetricCard
              label="总 Token"
              value={renderNumber(summary.total_tokens || 0)}
              icon="pie chart"
              description="全部完成请求的 Token 计数总计"
            />
            <MetricCard
              label="输入 Token"
              value={renderNumber(summary.input_tokens || 0)}
              icon="arrow down"
              description="Prompt Token（含缓存读取）"
            />
            <MetricCard
              label="输出 Token"
              value={renderNumber(summary.output_tokens || 0)}
              icon="arrow up"
              description="Completion 生成 Token"
            />
            <RateMetric
              label="Token 缓存命中率"
              value={summary.cache_hit_rate}
              hint="Σ 缓存读取 / Σ 已上报调用的输入 Token"
              description={`读取: ${renderNumber(summary.cache_read_input_tokens || 0)} Token`}
              status={summary.cache_hit_rate !== null ? 'success' : 'default'}
              icon="shield"
            />
          </div>

          <DataQualityNotice summary={summary} />

          {/* 趋势图 */}
          <ChartPanel
            title="Token 用量每日趋势"
            subtitle="聚合粒度按天呈现"
            empty={!trend || trend.length === 0}
          >
            <ResponsiveContainer width="100%" height={320}>
              <LineChart data={trend}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--app-border)" />
                <XAxis dataKey="date" stroke="var(--app-text-muted)" />
                <YAxis stroke="var(--app-text-muted)" tickFormatter={(v) => renderNumber(v)} />
                <Tooltip
                  formatter={(v) => renderNumber(v)}
                  contentStyle={{ backgroundColor: 'var(--app-surface-raised)', borderColor: 'var(--app-border-strong)', color: 'var(--app-text)' }}
                />
                <Legend />
                <Line type="monotone" dataKey="total_tokens" name="总 Token" stroke="#3b82f6" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="input_tokens" name="输入 Token" stroke="#10b981" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="output_tokens" name="输出 Token" stroke="#f59e0b" strokeWidth={2} dot={false} />
                <Line type="monotone" dataKey="cache_read_tokens" name="缓存读取" stroke="#8b5cf6" strokeWidth={2} dot={false} />
              </LineChart>
            </ResponsiveContainer>
          </ChartPanel>

          {/* 管理员多维排行面板 */}
          {admin && (
            <ChartPanel
              title="消耗维度排行榜 (Top 10)"
              subtitle="按模型、用户与渠道细分排行"
            >
              <Tab menu={{ secondary: true, pointing: true, inverted: true }} panes={panes} />
            </ChartPanel>
          )}
        </>
      )}
    </div>
  );
}
