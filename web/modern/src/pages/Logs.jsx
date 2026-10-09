import React, { useState, useEffect } from 'react';
import { 
  FileText, 
  Search, 
  Copy, 
  Check, 
  Clock, 
  Zap,
  CheckCircle2,
  XCircle
} from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { API } from '@/lib/api';
import { formatDate, formatNumber, copyToClipboard } from '@/lib/utils';
import { toast } from 'react-toastify';

export default function LogPage() {
  const [logs, setLogs] = useState([]);
  const [loading, setLoading] = useState(true);
  const [copiedId, setCopiedId] = useState(null);

  const loadLogs = async () => {
    setLoading(true);
    try {
      const res = await API.get('/api/log/?p=0');
      if (res.data?.success) {
        setLogs(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadLogs();
  }, []);

  const handleCopy = async (text) => {
    const ok = await copyToClipboard(text);
    if (ok) {
      setCopiedId(text);
      toast.success('已复制请求 ID');
      setTimeout(() => setCopiedId(null), 2000);
    } else {
      toast.error('复制失败，请手动选择并复制');
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">调用日志</h1>
          <p className="text-sm text-muted-foreground mt-1">查看详细请求记录、Token 输入输出分布与链路耗时</p>
        </div>
        <Button variant="outline" size="sm" onClick={loadLogs}>刷新日志</Button>
      </div>

      <Card className="bg-card/40 border-border/60">
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-xs text-left">
              <thead className="border-b border-border text-muted-foreground bg-muted/20">
                <tr>
                  <th className="py-3 px-4">请求时间</th>
                  <th className="py-3 px-4">模型</th>
                  <th className="py-3 px-4">提示词 Token</th>
                  <th className="py-3 px-4">补全 Token</th>
                  <th className="py-3 px-4">耗时</th>
                  <th className="py-3 px-4">请求 ID</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                {logs.map((log) => (
                  <tr key={log.id} className="hover:bg-muted/30 transition-colors">
                    <td className="py-3 px-4 text-muted-foreground">{formatDate(log.created_at)}</td>
                    <td className="py-3 px-4 font-mono font-medium text-foreground">{log.model_name || '—'}</td>
                    <td className="py-3 px-4 font-mono text-emerald-400">{formatNumber(log.prompt_tokens)}</td>
                    <td className="py-3 px-4 font-mono text-amber-400">{formatNumber(log.completion_tokens)}</td>
                    <td className="py-3 px-4 font-mono text-muted-foreground">{log.elapsed_time ? `${log.elapsed_time}ms` : '—'}</td>
                    <td className="py-3 px-4 font-mono text-muted-foreground">
                      {log.request_id ? (
                        <div className="flex items-center gap-1.5">
                          <span className="truncate max-w-[120px]">{log.request_id}</span>
                          <button
                            onClick={() => handleCopy(log.request_id)}
                            className="text-muted-foreground hover:text-foreground"
                          >
                            {copiedId === log.request_id ? <Check className="w-3 h-3 text-emerald-400" /> : <Copy className="w-3 h-3" />}
                          </button>
                        </div>
                      ) : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
