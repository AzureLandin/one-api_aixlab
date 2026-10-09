import React, { useEffect, useState, useCallback } from 'react';
import { Grid, Table, Label } from 'semantic-ui-react';
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
import { getUsageSummary, getUsageTrend, getUsageRanking } from '../../services/usage';

// 共享组件引入
import PageHeader from '../../components/layout/PageHeader';
import MetricCard from '../../components/analytics/MetricCard';
import RateMetric from '../../components/analytics/RateMetric';
import ChartPanel from '../../components/analytics/ChartPanel';
import FilterBar from '../../components/analytics/FilterBar';
import DataQualityNotice from '../../components/analytics/DataQualityNotice';
import PageState from '../../components/states/PageState';

const chartColors = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899', '#06b6d4'];

const Dashboard = () => {
  const admin = isAdmin();
  const [days, setDays] = useState(7);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  const [denied, setDenied] = useState(false);

  const [summary, setSummary] = useState(null);
  const [trend, setTrend] = useState([]);
  const [byModel, setByModel] = useState([]);

  const computeTimeRange = useCallback(() => {
    if (days === 0) return {};
    const end = Math.floor(Date.now() / 1000);
    const start = end - days * 86400;
    return { start_timestamp: start, end_timestamp: end };
  }, [days]);

  const loadData = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    else setLoading(true);
    setError(false);
    setDenied(false);

    const filters = computeTimeRange();

    try {
      const [sumRes, trendRes] = await Promise.all([
        getUsageSummary(filters),
        getUsageTrend(filters),
      ]);

      if (sumRes.success) setSummary(sumRes.data);
      if (trendRes.success) setTrend(trendRes.data || []);

      if (admin) {
        const byRes = await getUsageRanking('model', { ...filters, limit: 8 });
        if (byRes.success) setByModel(byRes.data || []);
      } else {
        setByModel([]);
      }
    } catch (e) {
      const status = e?.response?.status;
      if (status === 401 || status === 403) setDenied(true);
      else setError(true);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [admin, computeTimeRange]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  if (loading && !summary) {
    return <PageState type="loading" title="正在加载用量数据..." />;
  }

  if (denied) {
    return <PageState type="forbidden" title="无权访问" message="您当前无权查看该用量看板统计" />;
  }

  if (error && !summary) {
    return (
      <PageState
        type="error"
        title="数据加载失败"
        message="无法连接至用量统计服务，请检查网络后重试"
        onRetry={() => loadData(false)}
      />
    );
  }

  return (
    <div className="dashboard-container">
      <PageHeader
        title="总览看板"
        description="实时监控系统 Token 消耗事实与 Prompt Caching 缓存效率"
      />

      <FilterBar
        days={days}
        onChangeDays={setDays}
        onRefresh={() => loadData(true)}
        loading={refreshing}
      />

      {summary && (
        <>
          {/* Token 概览卡片 */}
          <div className="metric-cards-grid">
            <MetricCard
              label="总 Token"
              value={renderNumber(summary.total_tokens || 0)}
              icon="pie chart"
              description="全部请求生成与输入的 Token 总和"
            />
            <MetricCard
              label="输入 Token"
              value={renderNumber(summary.input_tokens || 0)}
              icon="arrow down"
              description="用户输入的 Prompt Token（含缓存读取）"
            />
            <MetricCard
              label="输出 Token"
              value={renderNumber(summary.output_tokens || 0)}
              icon="arrow up"
              description="模型生成的 Completion Token"
            />
            <MetricCard
              label="请求调用次数"
              value={renderNumber(summary.request_count || 0)}
              icon="server"
              description="成功与处理完成的请求总数"
            />
          </div>

          {/* 缓存三指标卡片（§13 重点验收项：null 显示为 "—"，绝不当 0%） */}
          <div className="metric-cards-grid" style={{ marginTop: '14px' }}>
            <RateMetric
              label="Token 缓存命中率"
              value={summary.cache_hit_rate}
              hint="Σ 缓存读取 Token / Σ 已报告缓存调用的输入 Token"
              description={`已读取: ${renderNumber(summary.cache_read_input_tokens || 0)} Token`}
              status={summary.cache_hit_rate !== null ? 'success' : 'default'}
              icon="shield"
            />
            <RateMetric
              label="调用缓存命中率"
              value={summary.call_hit_rate}
              hint="命中缓存的调用次数 / 已报告缓存的成功调用总数"
              description="按次统计产生缓存读取的请求比例"
              status={summary.call_hit_rate !== null ? 'success' : 'default'}
              icon="crosshairs"
            />
            <RateMetric
              label="缓存报告覆盖率"
              value={summary.cache_coverage}
              hint="已报告缓存数据的调用次数 / 全部生成调用总数"
              description="上游模型适配器上报 Prompt Caching 的比例"
              status={summary.cache_coverage !== null ? 'warning' : 'default'}
              icon="check circle outline"
            />
            <MetricCard
              label="缓存写入 Token"
              value={renderNumber(summary.cache_write_input_tokens || 0)}
              icon="write"
              description="首次创建或更新上下文缓存的 Token 数"
            />
          </div>

          <DataQualityNotice summary={summary} />

          {/* 趋势图 */}
          <ChartPanel
            title="Token 用量每日趋势"
            subtitle="输入、输出与缓存读取历史趋势对比"
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

          {/* 管理员专属排行面板 */}
          {admin && byModel && byModel.length > 0 && (
            <ChartPanel
              title="模型用量分布 (Top 8)"
              subtitle="按模型消耗聚合分析及缓存命中表现"
            >
              <Grid columns={2} stackable>
                <Grid.Column width={10}>
                  <ResponsiveContainer width="100%" height={280}>
                    <BarChart data={byModel}>
                      <CartesianGrid strokeDasharray="3 3" stroke="var(--app-border)" />
                      <XAxis dataKey="model_name" stroke="var(--app-text-muted)" />
                      <YAxis stroke="var(--app-text-muted)" tickFormatter={(v) => renderNumber(v)} />
                      <Tooltip
                        formatter={(v) => renderNumber(v)}
                        contentStyle={{ backgroundColor: 'var(--app-surface-raised)', borderColor: 'var(--app-border-strong)', color: 'var(--app-text)' }}
                      />
                      <Legend />
                      <Bar dataKey="input_tokens" name="输入 Token" fill="#10b981" stackId="a" />
                      <Bar dataKey="output_tokens" name="输出 Token" fill="#f59e0b" stackId="a" />
                    </BarChart>
                  </ResponsiveContainer>
                </Grid.Column>
                <Grid.Column width={6}>
                  <div className="table-scroll">
                    <Table basic="very" inverted compact size="small">
                      <Table.Header>
                        <Table.Row>
                          <Table.HeaderCell>模型名称</Table.HeaderCell>
                          <Table.HeaderCell textAlign="right">总 Token</Table.HeaderCell>
                          <Table.HeaderCell textAlign="right">命中率</Table.HeaderCell>
                        </Table.Row>
                      </Table.Header>
                      <Table.Body>
                        {byModel.map((item, idx) => (
                          <Table.Row key={item.model_name || idx}>
                            <Table.Cell>
                              <span style={{ fontFamily: 'var(--app-font-mono)' }}>{item.model_name}</span>
                            </Table.Cell>
                            <Table.Cell textAlign="right">{renderNumber(item.total_tokens)}</Table.Cell>
                            <Table.Cell textAlign="right">
                              <Label size="tiny" basic color={item.cache_hit_rate > 0 ? 'green' : 'grey'}>
                                {item.cache_hit_rate !== null ? `${(item.cache_hit_rate * 100).toFixed(1)}%` : '—'}
                              </Label>
                            </Table.Cell>
                          </Table.Row>
                        ))}
                      </Table.Body>
                    </Table>
                  </div>
                </Grid.Column>
              </Grid>
            </ChartPanel>
          )}
        </>
      )}
    </div>
  );
};

export default Dashboard;
