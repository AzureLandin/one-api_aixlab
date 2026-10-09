import React, { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { 
  LineChart as LineChartIcon,
  RefreshCw,
  Search,
  Filter
} from 'lucide-react';
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Badge } from '@/components/ui/Badge';
import { getUsageSummary, getUsageTrend, getUsageRanking, parseUsageFilters } from '@/lib/usage';
import { formatNumber, formatRate } from '@/lib/utils';
import { useAuth } from '@/context/AuthContext';
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend
} from 'recharts';

export default function Usage() {
  const { isAdmin } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();

  const initialFilters = parseUsageFilters(searchParams);
  const [userId, setUserId] = useState(initialFilters.user_id || '');
  const [days, setDays] = useState(initialFilters.start_timestamp ? 0 : 7);
  const [tab, setTab] = useState('model'); // 'model' | 'user' | 'channel'

  const [loading, setLoading] = useState(true);
  const [summary, setSummary] = useState(null);
  const [trend, setTrend] = useState([]);
  const [rankingData, setRankingData] = useState([]);

  const loadData = useCallback(async () => {
    setLoading(true);

    const filters = {};
    if (days !== 0) {
      const end = Math.floor(Date.now() / 1000);
      const start = end - days * 86400;
      filters.start_timestamp = start;
      filters.end_timestamp = end;
    }
    if (isAdmin && userId.trim()) {
      filters.user_id = userId.trim();
    }

    // 同步 URL 参数
    const nextParams = new URLSearchParams();
    if (filters.start_timestamp) nextParams.set('start_timestamp', filters.start_timestamp);
    if (filters.end_timestamp) nextParams.set('end_timestamp', filters.end_timestamp);
    if (filters.user_id) nextParams.set('user_id', filters.user_id);
    setSearchParams(nextParams, { replace: true });

    try {
      const [sumRes, trendRes] = await Promise.all([
        getUsageSummary(filters),
        getUsageTrend(filters),
      ]);

      if (sumRes.success) setSummary(sumRes.data);
      if (trendRes.success) setTrend(trendRes.data || []);

      if (isAdmin) {
        const rankRes = await getUsageRanking(tab, { ...filters, limit: 15 });
        if (rankRes.success) setRankingData(rankRes.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  }, [days, userId, tab, isAdmin, setSearchParams]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">用量分析</h1>
          <p className="text-sm text-muted-foreground mt-1">支持深链接检索、时间范围过滤与模型/用户/渠道三维度细分排行</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {isAdmin && (
            <div className="relative w-40">
              <Input
                placeholder="User ID 过滤"
                value={userId}
                onChange={(e) => setUserId(e.target.value)}
                className="h-8 text-xs pl-7"
              />
              <Search className="w-3.5 h-3.5 text-muted-foreground absolute left-2 top-2.5" />
            </div>
          )}
          <div className="inline-flex rounded-lg border border-border p-1 bg-card/60">
            {[7, 30, 90, 0].map((d) => (
              <button
                key={d}
                onClick={() => setDays(d)}
                className={`px-3 py-1 text-xs font-medium rounded-md transition-colors ${
                  days === d ? 'bg-primary text-primary-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {d === 0 ? '全部' : `${d} 天`}
              </button>
            ))}
          </div>
          <Button variant="outline" size="sm" onClick={loadData}>
            <RefreshCw className="w-3.5 h-3.5 mr-1" /> 刷新
          </Button>
        </div>
      </div>

      {/* 指标卡片 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <Card className="bg-card/40">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase">总 Token</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-foreground">{formatNumber(summary?.total_tokens)}</div>
          </CardContent>
        </Card>
        <Card className="bg-card/40">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase">输入 Token</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-emerald-400">{formatNumber(summary?.input_tokens)}</div>
          </CardContent>
        </Card>
        <Card className="bg-card/40">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase">输出 Token</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-amber-400">{formatNumber(summary?.output_tokens)}</div>
          </CardContent>
        </Card>
        <Card className="bg-card/40">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase">缓存命中率</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold text-purple-400">{formatRate(summary?.cache_hit_rate)}</div>
          </CardContent>
        </Card>
      </div>

      {/* 趋势图 */}
      <Card className="bg-card/40">
        <CardHeader>
          <CardTitle className="text-base font-semibold">历史用量趋势图</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="h-64 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={trend} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                <XAxis dataKey="date" stroke="hsl(var(--muted-foreground))" fontSize={11} />
                <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickFormatter={formatNumber} />
                <Tooltip 
                  contentStyle={{ backgroundColor: 'hsl(var(--card))', borderColor: 'hsl(var(--border))', borderRadius: '8px' }}
                  formatter={formatNumber}
                />
                <Legend />
                <Area type="monotone" dataKey="total_tokens" name="总 Token" stroke="#3b82f6" fill="#3b82f6" fillOpacity={0.1} />
                <Area type="monotone" dataKey="input_tokens" name="输入 Token" stroke="#10b981" fill="#10b981" fillOpacity={0.1} />
                <Area type="monotone" dataKey="output_tokens" name="输出 Token" stroke="#f59e0b" fillOpacity={0} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </CardContent>
      </Card>

      {/* 管理员三维排行 */}
      {isAdmin && (
        <Card className="bg-card/40">
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="text-base font-semibold">用量排行榜</CardTitle>
              <CardDescription className="text-xs">按模型、用户与渠道细分排行</CardDescription>
            </div>
            <div className="flex gap-1 border border-border p-1 rounded-lg">
              <button
                onClick={() => setTab('model')}
                className={`px-3 py-1 text-xs rounded-md transition-colors ${tab === 'model' ? 'bg-primary text-primary-foreground font-medium' : 'text-muted-foreground'}`}
              >
                模型排行
              </button>
              <button
                onClick={() => setTab('user')}
                className={`px-3 py-1 text-xs rounded-md transition-colors ${tab === 'user' ? 'bg-primary text-primary-foreground font-medium' : 'text-muted-foreground'}`}
              >
                用户排行
              </button>
              <button
                onClick={() => setTab('channel')}
                className={`px-3 py-1 text-xs rounded-md transition-colors ${tab === 'channel' ? 'bg-primary text-primary-foreground font-medium' : 'text-muted-foreground'}`}
              >
                渠道排行
              </button>
            </div>
          </CardHeader>
          <CardContent>
            <div className="overflow-x-auto">
              <table className="w-full text-xs text-left">
                <thead className="border-b border-border text-muted-foreground">
                  <tr>
                    <th className="py-2.5 px-3">名称 / 标识</th>
                    <th className="py-2.5 px-3 text-right">输入 Token</th>
                    <th className="py-2.5 px-3 text-right">输出 Token</th>
                    <th className="py-2.5 px-3 text-right">总 Token</th>
                    <th className="py-2.5 px-3 text-right">缓存命中率</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/60">
                  {rankingData.map((item, idx) => (
                    <tr key={idx} className="hover:bg-muted/30 transition-colors">
                      <td className="py-2.5 px-3 font-mono font-medium text-foreground">
                        {item.channel_name ? `${item.channel_name} (#${item.channel_id})` : item.username ? `${item.username} (#${item.user_id})` : item.model_name || `ID: ${item.user_id || item.channel_id}`}
                      </td>
                      <td className="py-2.5 px-3 text-right text-muted-foreground">{formatNumber(item.input_tokens)}</td>
                      <td className="py-2.5 px-3 text-right text-muted-foreground">{formatNumber(item.output_tokens)}</td>
                      <td className="py-2.5 px-3 text-right font-medium text-foreground">{formatNumber(item.total_tokens)}</td>
                      <td className="py-2.5 px-3 text-right">
                        <Badge variant={item.cache_hit_rate > 0 ? 'success' : 'secondary'}>
                          {formatRate(item.cache_hit_rate)}
                        </Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
