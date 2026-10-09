import React, { useState, useEffect } from 'react';
import { 
  KeyRound, 
  Plus, 
  Copy, 
  Check, 
  Trash2, 
  Eye, 
  EyeOff, 
  AlertCircle 
} from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Badge } from '@/components/ui/Badge';
import { API } from '@/lib/api';
import { formatDate, formatNumber, copyToClipboard } from '@/lib/utils';
import { toast } from 'react-toastify';

export default function TokenPage() {
  const [tokens, setTokens] = useState([]);
  const [loading, setLoading] = useState(true);
  const [copiedKey, setCopiedKey] = useState(null);
  const [showKeyMap, setShowKeyMap] = useState({});

  // 创建令牌弹窗状态
  const [createOpen, setCreateOpen] = useState(false);
  const [newTokenName, setNewTokenName] = useState('');

  const loadTokens = async () => {
    setLoading(true);
    try {
      const res = await API.get('/api/token/?p=0&order=');
      if (res.data?.success) {
        setTokens(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadTokens();
  }, []);

  const handleCopy = async (key) => {
    const fullKey = `sk-${key}`;
    const ok = await copyToClipboard(fullKey);
    if (ok) {
      setCopiedKey(key);
      toast.success('已复制 API Key 到剪贴板');
      setTimeout(() => setCopiedKey(null), 2000);
    } else {
      toast.error('复制失败，请手动选择并复制');
    }
  };

  const handleToggleStatus = async (id, currentStatus) => {
    const action = currentStatus === 1 ? 'disable' : 'enable';
    try {
      const res = await API.post('/api/token/manage', { id, action });
      if (res.data?.success) {
        toast.success(action === 'disable' ? '令牌已禁用' : '令牌已启用');
        loadTokens();
      }
    } catch {}
  };

  const handleDelete = async (id, name) => {
    if (!window.confirm(`确定彻底删除令牌 [${name}] 吗？此操作无法撤销。`)) return;
    try {
      const res = await API.delete(`/api/token/${id}`);
      if (res.data?.success) {
        toast.success('令牌已删除');
        loadTokens();
      }
    } catch {}
  };

  const handleCreateToken = async () => {
    if (!newTokenName.trim()) {
      toast.warn('请输入令牌名称');
      return;
    }
    try {
      const res = await API.post('/api/token/', {
        name: newTokenName.trim(),
        expired_time: -1,
        remain_quota: 0,
        unlimited_quota: true,
        models: '',
        subnet: '',
      });
      if (res.data?.success) {
        toast.success('令牌创建成功！');
        setNewTokenName('');
        setCreateOpen(false);
        loadTokens();
      }
    } catch {}
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">API 令牌</h1>
          <p className="text-sm text-muted-foreground mt-1">管理用于 Relay 访问和第三方客户端集成的安全 API Key</p>
        </div>
        <Button onClick={() => setCreateOpen(true)} className="gap-2">
          <Plus className="w-4 h-4" /> 添加令牌
        </Button>
      </div>

      {/* 创建令牌抽屉/表单 */}
      {createOpen && (
        <Card className="bg-card border-primary/30 shadow-lg p-4">
          <div className="flex items-center justify-between mb-4">
            <h3 className="font-semibold text-sm">创建新令牌</h3>
            <button onClick={() => setCreateOpen(false)} className="text-muted-foreground text-xs hover:text-foreground">取消</button>
          </div>
          <div className="flex gap-3">
            <Input
              placeholder="例如：开发环境专属 Key"
              value={newTokenName}
              onChange={(e) => setNewTokenName(e.target.value)}
              className="max-w-md text-sm"
            />
            <Button onClick={handleCreateToken}>确认创建</Button>
          </div>
        </Card>
      )}

      {/* 令牌列表 */}
      <Card className="bg-card/40 border-border/60">
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-xs text-left">
              <thead className="border-b border-border text-muted-foreground bg-muted/20">
                <tr>
                  <th className="py-3 px-4">名称</th>
                  <th className="py-3 px-4">状态</th>
                  <th className="py-3 px-4">API Key</th>
                  <th className="py-3 px-4">调用次数</th>
                  <th className="py-3 px-4">创建时间</th>
                  <th className="py-3 px-4 text-right">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                {tokens.map((token) => {
                  const showKey = showKeyMap[token.id];
                  const maskedKey = showKey ? `sk-${token.key}` : `sk-${token.key.slice(0, 6)}••••••••••••${token.key.slice(-4)}`;

                  return (
                    <tr key={token.id} className="hover:bg-muted/30 transition-colors">
                      <td className="py-3 px-4 font-medium text-foreground">
                        {token.name}
                      </td>
                      <td className="py-3 px-4">
                        <Badge variant={token.status === 1 ? 'success' : 'secondary'}>
                          {token.status === 1 ? '已启用' : '已禁用'}
                        </Badge>
                      </td>
                      <td className="py-3 px-4 font-mono text-muted-foreground">
                        <div className="flex items-center gap-2">
                          <span>{maskedKey}</span>
                          <button
                            onClick={() => setShowKeyMap((prev) => ({ ...prev, [token.id]: !prev[token.id] }))}
                            className="text-muted-foreground hover:text-foreground"
                          >
                            {showKey ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                          </button>
                        </div>
                      </td>
                      <td className="py-3 px-4 text-muted-foreground">{formatNumber(token.request_count)}</td>
                      <td className="py-3 px-4 text-muted-foreground">{formatDate(token.created_time)}</td>
                      <td className="py-3 px-4 text-right space-x-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => handleCopy(token.key)}
                          className="h-7 px-2 text-xs"
                        >
                          {copiedKey === token.key ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => handleToggleStatus(token.id, token.status)}
                          className="h-7 px-2 text-xs"
                        >
                          {token.status === 1 ? '禁用' : '启用'}
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => handleDelete(token.id, token.name)}
                          className="h-7 px-2 text-xs text-destructive hover:bg-destructive/10"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
