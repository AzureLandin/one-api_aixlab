import React, { useEffect, useState, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Card,
  Grid,
  Dropdown,
  Button,
  Icon,
  Message,
  Table,
  Label,
} from 'semantic-ui-react';
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
import { API, isAdmin } from '../../helpers';
import { renderNumber } from '../../helpers/render';
import './Dashboard.css';

const chartColors = ['#4318FF', '#00B5D8', '#6C63FF', '#05CD99', '#FFB547', '#FF5E7D'];

// fmtRate renders a nullable ratio as a percentage, or a dash when the metric
// is not computable (unreported cache data must NOT show as 0% — §13).
function fmtRate(v) {
  if (v === null || v === undefined) return '—';
  return (v * 100).toFixed(1) + '%';
}

const RANGE_OPTIONS = [
  { key: '7', value: 7, text: 'range.7d' },
  { key: '30', value: 30, text: 'range.30d' },
  { key: '90', value: 90, text: 'range.90d' },
  { key: '0', value: 0, text: 'range.all' },
];

const Dashboard = () => {
  const { t } = useTranslation();
  const admin = isAdmin();
  const base = admin ? '/api/usage' : '/api/usage/self';

  const [days, setDays] = useState(7);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [denied, setDenied] = useState(false);

  const [summary, setSummary] = useState(null);
  const [trend, setTrend] = useState([]);
  const [byModel, setByModel] = useState([]);

  const rangeParams = useCallback(() => {
    if (days === 0) return '';
    const end = Math.floor(Date.now() / 1000);
    const start = end - days * 86400;
    return `?start_timestamp=${start}&end_timestamp=${end}`;
  }, [days]);

  const load = useCallback(async () => {
    setLoading(true);
    setError(false);
    setDenied(false);
    try {
      const q = rangeParams();
      const [sumRes, trendRes] = await Promise.all([
        API.get(`${base}/summary${q}`),
        API.get(`${base}/trend${q}`),
      ]);
      if (sumRes.data.success) setSummary(sumRes.data.data);
      if (trendRes.data.success) setTrend(trendRes.data.data || []);

      if (admin) {
        const byRes = await API.get(`${base}/by-model${q ? q + '&limit=8' : '?limit=8'}`);
        if (byRes.data.success) setByModel(byRes.data.data || []);
      } else {
        setByModel([]);
      }
    } catch (e) {
      const status = e?.response?.status;
      if (status === 401 || status === 403) setDenied(true);
      else setError(true);
      setSummary(null);
      setTrend([]);
      setByModel([]);
    } finally {
      setLoading(false);
    }
  }, [base, admin, rangeParams]);

  useEffect(() => {
    load();
  }, [load]);

  const trendData = trend.map((r) => ({
    date: r.day,
    tokens: r.summary.total_tokens,
    requests: r.summary.request_count,
    cache_read: r.summary.cache_read_input_tokens,
  }));

  const modelData = byModel.map((r) => ({
    name: r.model_name,
    tokens: r.summary.total_tokens,
  }));

  const cacheReported = summary && (summary.cache_hit_rate !== null || summary.not_reported_cache_calls === 0);

  return (
    <div className='dashboard-container'>
      {/* toolbar: range + refresh */}
      <Card fluid className='chart-card'>
        <Card.Content>
          <Grid stackable columns='equal'>
            <Grid.Column width={10}>
              <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                <span style={{ fontWeight: 600 }}>{t('dashboard.title')}</span>
                <Dropdown
                  selection
                  value={days}
                  options={RANGE_OPTIONS.map((o) => ({ ...o, text: t(`dashboard.${o.text}`) }))}
                  onChange={(_, { value }) => setDays(value)}
                />
              </div>
            </Grid.Column>
            <Grid.Column width={6} textAlign='right'>
              <Button icon labelPosition='left' primary onClick={load} loading={loading}>
                <Icon name='refresh' />
                {t('dashboard.refresh')}
              </Button>
            </Grid.Column>
          </Grid>
        </Card.Content>
      </Card>

      {denied && (
        <Message negative>
          <Message.Header>{t('dashboard.error.no_permission')}</Message.Header>
        </Message>
      )}
      {error && (
        <Message negative>
          <Message.Header>{t('dashboard.error.load')}</Message.Header>
        </Message>
      )}

      {loading && !summary && (
        <Card fluid className='chart-card'>
          <Card.Content>
            <Icon loading name='spinner' /> {t('dashboard.loading')}
          </Card.Content>
        </Card>
      )}

      {summary && (
        <>
          {/* token overview */}
          <Grid columns={4} stackable className='charts-grid'>
            <StatCard label={t('dashboard.cards.total_tokens')} value={renderNumber(summary.total_tokens)} />
            <StatCard label={t('dashboard.cards.input_tokens')} value={renderNumber(summary.input_tokens)} />
            <StatCard label={t('dashboard.cards.output_tokens')} value={renderNumber(summary.output_tokens)} />
            <StatCard label={t('dashboard.cards.requests')} value={renderNumber(summary.request_count)} />
          </Grid>

          {/* cache three-metric cards (§9.2) */}
          <Grid columns={3} stackable className='charts-grid'>
            <MetricCard
              t={t}
              label={t('dashboard.cache.hit_rate')}
              value={fmtRate(summary.cache_hit_rate)}
              hint={t('dashboard.cache.hit_rate_hint')}
              reported={cacheReported}
            />
            <MetricCard
              t={t}
              label={t('dashboard.cache.call_hit_rate')}
              value={fmtRate(summary.call_hit_rate)}
              hint={t('dashboard.cache.call_hit_rate_hint')}
              reported={cacheReported}
            />
            <MetricCard
              t={t}
              label={t('dashboard.cache.coverage')}
              value={fmtRate(summary.cache_coverage)}
              hint={t('dashboard.cache.coverage_hint', {
                reported: summary.not_reported_cache_calls,
              })}
              reported={cacheReported}
            />
          </Grid>

          {summary.not_reported_cache_calls > 0 && (
            <Message info>
              {t('dashboard.cache.unreported_notice', {
                count: summary.not_reported_cache_calls,
              })}
            </Message>
          )}

          {/* trend */}
          <Card fluid className='chart-card'>
            <Card.Content>
              <Card.Header>{t('dashboard.charts.tokens.title')}</Card.Header>
              <div className='chart-container'>
                <ResponsiveContainer width='100%' height={260}>
                  <LineChart data={trendData}>
                    <CartesianGrid strokeDasharray='3 3' vertical={false} opacity={0.1} />
                    <XAxis dataKey='date' axisLine={false} tickLine={false} tick={{ fontSize: 12, fill: '#A3AED0' }} />
                    <YAxis axisLine={false} tickLine={false} tick={{ fontSize: 12, fill: '#A3AED0' }} />
                    <Tooltip />
                    <Legend />
                    <Line type='monotone' dataKey='tokens' stroke={chartColors[0]} strokeWidth={2} dot={false}
                      name={t('dashboard.charts.tokens.tooltip')} />
                    <Line type='monotone' dataKey='cache_read' stroke={chartColors[3]} strokeWidth={2} dot={false}
                      name={t('dashboard.cache.read')} />
                  </LineChart>
                </ResponsiveContainer>
              </div>
            </Card.Content>
          </Card>

          {/* by-model ranking (admin only) */}
          {admin && (
            <Card fluid className='chart-card'>
              <Card.Content>
                <Card.Header>{t('dashboard.ranking.by_model')}</Card.Header>
                {modelData.length === 0 ? (
                  <Message>{t('dashboard.empty')}</Message>
                ) : (
                  <>
                    <div className='chart-container'>
                      <ResponsiveContainer width='100%' height={240}>
                        <BarChart data={modelData}>
                          <CartesianGrid strokeDasharray='3 3' vertical={false} opacity={0.1} />
                          <XAxis dataKey='name' axisLine={false} tickLine={false} tick={{ fontSize: 12, fill: '#A3AED0' }} />
                          <YAxis axisLine={false} tickLine={false} tick={{ fontSize: 12, fill: '#A3AED0' }} />
                          <Tooltip />
                          <Bar dataKey='tokens' fill={chartColors[0]} radius={[4, 4, 0, 0]}
                            name={t('dashboard.charts.tokens.tooltip')} />
                        </BarChart>
                      </ResponsiveContainer>
                    </div>
                    <Table celled style={{ overflowX: 'auto' }}>
                      <Table.Header>
                        <Table.Row>
                          <Table.HeaderCell>{t('dashboard.table.model')}</Table.HeaderCell>
                          <Table.HeaderCell>{t('dashboard.cards.total_tokens')}</Table.HeaderCell>
                          <Table.HeaderCell>{t('dashboard.cards.requests')}</Table.HeaderCell>
                          <Table.HeaderCell>{t('dashboard.cache.hit_rate')}</Table.HeaderCell>
                          <Table.HeaderCell>{t('dashboard.cache.coverage')}</Table.HeaderCell>
                        </Table.Row>
                      </Table.Header>
                      <Table.Body>
                        {byModel.map((r, i) => (
                          <Table.Row key={i}>
                            <Table.Cell>{r.model_name}</Table.Cell>
                            <Table.Cell>{renderNumber(r.summary.total_tokens)}</Table.Cell>
                            <Table.Cell>{renderNumber(r.summary.request_count)}</Table.Cell>
                            <Table.Cell>
                              {fmtRate(r.summary.cache_hit_rate)}
                              {r.summary.cache_hit_rate === null && (
                                <Label basic size='mini' style={{ marginLeft: 6 }}>{t('dashboard.cache.na')}</Label>
                              )}
                            </Table.Cell>
                            <Table.Cell>{fmtRate(r.summary.cache_coverage)}</Table.Cell>
                          </Table.Row>
                        ))}
                      </Table.Body>
                    </Table>
                  </>
                )}
              </Card.Content>
            </Card>
          )}
        </>
      )}

      {!loading && summary && summary.request_count === 0 && (
        <Message>{t('dashboard.empty')}</Message>
      )}
    </div>
  );
};

const StatCard = ({ label, value }) => (
  <Grid.Column>
    <Card fluid className='chart-card'>
      <Card.Content>
        <Card.Meta>{label}</Card.Meta>
        <div style={{ fontSize: '1.8rem', fontWeight: 700, marginTop: '6px' }}>{value}</div>
      </Card.Content>
    </Card>
  </Grid.Column>
);

const MetricCard = ({ t, label, value, hint, reported }) => (
  <Grid.Column>
    <Card fluid className='chart-card'>
      <Card.Content>
        <Card.Meta>{label}</Card.Meta>
        <div style={{ fontSize: '1.8rem', fontWeight: 700, margin: '6px 0' }}>{value}</div>
        <Card.Description style={{ fontSize: '0.8rem', color: '#888' }}>{hint}</Card.Description>
        {!reported && (
          <Label size='mini' basic color='grey' style={{ marginTop: '6px' }}>
            {t('dashboard.cache.not_reported')}
          </Label>
        )}
      </Card.Content>
    </Card>
  </Grid.Column>
);

export default Dashboard;
