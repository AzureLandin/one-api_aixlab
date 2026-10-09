import React, { useEffect, useState, useCallback } from 'react';
import { 
  Activity, 
  ArrowDownLeft, 
  ArrowUpRight, 
  Sparkles, 
  ShieldCheck, 
  RefreshCw,
  Zap,
  Info
} from 'lucide-react';
import { Card, CardHeader, CardTitle, CardDescription, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { getUsageSummary, getUsageTrend, getUsageRanking } from '@/lib/usage';
import { formatNumber, formatRate } from '@/lib/utils';
import { useAuth } from '@/context/AuthContext';
import {
  AreaChart,
  Area,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend
} from 'recharts';

export default function Dashboard() {
  const { isAdmin } = useAuth();
  const [days, setDays] = useState(7);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [summary, setSummary] = useState(null);
  const [trend, setTrend] = useState([]);
  const [byModel, setByModel] = useState([]);

  const loadData = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    else setLoading(true);

    const filters = {};
    if (days !== 0) {
      const end = Math.floor(Date.now() / 1000);
      const start = end - days * 86400;
      filters.start_timestamp = start;
      filters.end_timestamp = end;
    }

    try {
      const [sumRes, trendRes] = await Promise.all([
        getUsageSummary(filters),
        getUsageTrend(filters),
      ]);

      if (sumRes.success) setSummary(sumRes.data);
      if (trendRes.success) setTrend(trendRes.data || []);

      if (isAdmin) {
        const modelRes = await getUsageRanking('model', { ...filters, limit: 6 });
        if (modelRes.success) setByModel(modelRes.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [days, isAdmin]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  return (
    <div className="space-y-6">
      {/* 顶部标题与范围选择器 */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">总览看板</h1>
          <p className="text-sm text-muted-foreground mt-1">实时监控系统 Token 消耗事实与 Prompt Caching 命中效率</p>
        </div>
        <div className="flex items-center gap-2">
          <div className="inline-flex rounded-lg border border-border p-1 bg-card/60">
            {[7, 30, 90, 0].map((d) => (
              <button
                key={d}
                onClick={() => setDays(d)}
                className={`px-3 py-1 text-xs font-medium rounded-md transition-colors ${
                  days === d
                    ? 'bg-primary text-primary-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {d === 0 ? '全部' : `${d} 天`}
              </button>
            ))}
          </div>
          <Button variant="outline" size="icon" onClick={() => loadData(true)} disabled={refreshing}>
            <RefreshCw className={`w-4 h-4 ${refreshing ? 'animate-spin' : ''}`} />
          </Button>
        </div>
      </div>

      {/* 核心 Token 概览 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <Card className="bg-card/40 border-border/60 hover:border-border transition-colors">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">总 Token 消耗</CardTitle>
            <Activity className="w-4 h-4 text-primary" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight">{formatNumber(summary?.total_tokens)}</div>
            <p className="text-xs text-muted-foreground mt-1">所有完成调用的消耗总计</p>
          </CardContent>
        </Card>

        <Card className="bg-card/40 border-border/60 hover:border-border transition-colors">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">输入 Tokens (Prompt)</CardTitle>
            <ArrowDownLeft className="w-4 h-4 text-emerald-400" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight text-emerald-400">{formatNumber(summary?.input_tokens)}</div>
            <p className="text-xs text-muted-foreground mt-1">包含缓存读取的输入总数</p>
          </CardContent>
        </Card>

        <Card className="bg-card/40 border-border/60 hover:border-border transition-colors">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">输出 Tokens (Completion)</CardTitle>
            <ArrowUpRight className="w-4 h-4 text-amber-400" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight text-amber-400">{formatNumber(summary?.output_tokens)}</div>
            <p className="text-xs text-muted-foreground mt-1">模型实际生成的内容 Token</p>
          </CardContent>
        </Card>

        <Card className="bg-card/40 border-border/60 hover:border-border transition-colors">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">有效请求次数</CardTitle>
            <Zap className="w-4 h-4 text-blue-400" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight text-blue-400">{formatNumber(summary?.request_count)}</div>
            <p className="text-xs text-muted-foreground mt-1">成功落库的生成调用数量</p>
          </CardContent>
        </Card>
      </div>

      {/* 缓存三指标与数据质量（§13 重点契约：null 必为“—”） */}
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <Card className="bg-card/40 border-border/60">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Token 缓存命中率</CardTitle>
            <Sparkles className="w-4 h-4 text-purple-400" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight text-purple-400">{formatRate(summary?.cache_hit_rate)}</div>
            <div className="text-xs text-muted-foreground mt-1 flex items-center justify-between">
              <span>已命中读取:</span>
              <span className="font-mono">{formatNumber(summary?.cache_read_input_tokens)} Token</span>
            </div>
          </CardContent>
        </Card>

        <Card className="bg-card/40 border-border/60">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">调用缓存命中率</CardTitle>
            <ShieldCheck className="w-4 h-4 text-emerald-400" />
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight text-emerald-400">{formatRate(summary?.call_hit_rate)}</div>
            <p className="text-xs text-muted-foreground mt-1">命中缓存读取的请求占比</p>
          </CardContent>
        </Card>

        <Card className="bg-card/40 border-border/60">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">缓存上报覆盖率</CardTitle>
            <Badge variant="outline" className="text-[10px]">可信度指标</Badge>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-bold tracking-tight">{formatRate(summary?.cache_coverage)}</div>
            <p className="text-xs text-muted-foreground mt-1">上游渠道支持缓存字段的比例</p>
          </CardContent>
        </Card>
      </div>

      {/* 缓存未上报提示 */}
      {summary?.not_reported_cache_calls > 0 && (
        <div className="rounded-lg border border-border/80 bg-muted/30 px-4 py-3 flex items-start gap-3 text-xs text-muted-foreground">
          <Info className="w-4 h-4 text-primary mt-0.5 shrink-0" />
          <div>
            当前范围内共有 <span className="text-foreground font-semibold">{formatNumber(summary.not_reported_cache_calls)}</span> 次请求的上游模型未提供缓存统计字段，已据实计算覆盖率，可空指标展示为“—”。
          </div>
        </div>
      )}

      {/* 历史趋势图表 */}
      <Card className="bg-card/40 border-border/60">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Token 每日消耗趋势</CardTitle>
          <CardDescription>输入、输出与缓存读取历史趋势对比</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="h-72 w-full">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={trend} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
                <defs>
                  <linearGradient id="colorTotal" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.3}/>
                    <stop offset="95%" stopColor="#3b82f6" stopOpacity={0}/>
                  </linearGradient>
                  <linearGradient id="colorInput" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#10b981" stopOpacity={0.3}/>
                    <stop offset="95%" stopColor="#10b981" stopOpacity={0}/>
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                <XAxis dataKey="date" stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} />
                <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} tickFormatter={formatNumber} />
                <Tooltip 
                  contentStyle={{ 
                    backgroundColor: 'hsl(var(--card))', 
                    borderColor: 'hsl(var(--border))', 
                    borderRadius: '8px', 
                    fontSize: '12px' 
                  }} 
                  formatter={(val) => formatNumber(val)}
                />
                <Legend />
                <Area type="monotone" dataKey="total_tokens" name="总 Token" stroke="#3b82f6" fillOpacity={1} fill="url(#colorTotal)" />
                <Area type="monotone" dataKey="input_tokens" name="输入 Token" stroke="#10b981" fillOpacity={1} fill="url(#colorInput)" />
                <Area type="monotone" dataKey="output_tokens" name="输出 Token" stroke="#f59e0b" fillOpacity={0} />
                <Area type="monotone" dataKey="cache_read_tokens" name="缓存读取" stroke="#a855f7" fillOpacity={0} />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </CardContent>
      </Card>

      {/* 管理员模型排行 */}
      {isAdmin && byModel.length > 0 && (
        <Card className="bg-card/40 border-border/60">
          <CardHeader>
            <CardTitle className="text-base font-semibold">高频模型用量分布</CardTitle>
            <CardDescription>Top 6 模型的输入/输出及缓存命中表现</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
              <div className="h-64">
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={byModel} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
                    <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" vertical={false} />
                    <XAxis dataKey="model_name" stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} />
                    <YAxis stroke="hsl(var(--muted-foreground))" fontSize={11} tickLine={false} tickFormatter={formatNumber} />
                    <Tooltip 
                      contentStyle={{ backgroundColor: 'hsl(var(--card))', borderColor: 'hsl(var(--border))', borderRadius: '8px' }} 
                      formatter={formatNumber}
                    />
                    <Bar dataKey="input_tokens" name="输入 Token" fill="#10b981" radius={[4, 4, 0, 0]} />
                    <Bar dataKey="output_tokens" name="输出 Token" fill="#f59e0b" radius={[4, 4, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              </div>
              <div className="divide-y divide-border/60 overflow-y-auto max-h-64">
                {byModel.map((item, idx) => (
                  <div key={idx} className="py-2.5 flex items-center justify-between text-xs">
                    <span className="font-mono text-foreground font-medium">{item.model_name}</span>
                    <div className="flex items-center gap-3">
                      <span className="text-muted-foreground">{formatNumber(item.total_tokens)} Tokens</span>
                      <Badge variant={item.cache_hit_rate > 0 ? 'success' : 'secondary'}>
                        {formatRate(item.cache_hit_rate)}
                      </Badge>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
