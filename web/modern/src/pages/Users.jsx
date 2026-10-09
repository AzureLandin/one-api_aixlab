import React, { useState, useEffect } from 'react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { Input } from '@/components/ui/Input';
import { API } from '@/lib/api';
import { formatNumber } from '@/lib/utils';
import { Link } from 'react-router-dom';
import { ChevronLeft, ChevronRight, Search, RefreshCw } from 'lucide-react';

const PAGE_SIZE = 10;

export default function UsersPage() {
  const [users, setUsers] = useState([]);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [keyword, setKeyword] = useState('');

  const loadUsers = async (p = 0) => {
    setLoading(true);
    try {
      const res = await API.get(`/api/user/?p=${p}`);
      if (res.data?.success) {
        setUsers(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const searchUsers = async (kw) => {
    if (!kw.trim()) {
      loadUsers(0);
      return;
    }
    setLoading(true);
    try {
      const res = await API.get(`/api/user/search?keyword=${encodeURIComponent(kw.trim())}`);
      if (res.data?.success) {
        setUsers(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadUsers(page);
  }, [page]);

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">用户管理</h1>
          <p className="text-sm text-muted-foreground mt-1">管理系统注册用户、分配角色权限与直达用量统计</p>
        </div>
        <div className="flex items-center gap-2">
          <div className="relative w-64">
            <Input
              placeholder="搜索用户名或邮箱..."
              value={keyword}
              onChange={(e) => {
                setKeyword(e.target.value);
                if (!e.target.value) loadUsers(0);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') searchUsers(keyword);
              }}
              className="pl-8 text-xs bg-card/60"
            />
            <Search className="w-3.5 h-3.5 text-muted-foreground absolute left-2.5 top-3" />
          </div>
          <Button variant="outline" size="sm" onClick={() => loadUsers(page)}>
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
          </Button>
        </div>
      </div>

      <Card className="bg-card/40 border-border/60">
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-xs text-left">
              <thead className="border-b border-border text-muted-foreground bg-muted/20">
                <tr>
                  <th className="py-3 px-4">ID</th>
                  <th className="py-3 px-4">用户名</th>
                  <th className="py-3 px-4">角色</th>
                  <th className="py-3 px-4">状态</th>
                  <th className="py-3 px-4">累计请求数</th>
                  <th className="py-3 px-4 text-right">用量入口</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                {users.map((u) => (
                  <tr key={u.id} className="hover:bg-muted/30 transition-colors">
                    <td className="py-3 px-4 font-mono text-muted-foreground">{u.id}</td>
                    <td className="py-3 px-4 font-semibold text-foreground">{u.username}</td>
                    <td className="py-3 px-4">
                      <Badge variant={u.role >= 100 ? 'destructive' : u.role >= 10 ? 'warning' : 'outline'}>
                        {u.role >= 100 ? '超级管理员' : u.role >= 10 ? '管理员' : '普通用户'}
                      </Badge>
                    </td>
                    <td className="py-3 px-4">
                      <Badge variant={u.status === 1 ? 'success' : 'destructive'}>
                        {u.status === 1 ? '正常' : '已封禁'}
                      </Badge>
                    </td>
                    <td className="py-3 px-4 text-muted-foreground">{formatNumber(u.request_count)}</td>
                    <td className="py-3 px-4 text-right">
                      <Button variant="outline" size="sm" asChild className="h-7 px-2.5 text-xs">
                        <Link to={`/usage?user_id=${u.id}`}>查看用量</Link>
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* 分页控制栏 */}
          <div className="p-3 border-t border-border flex items-center justify-between text-xs text-muted-foreground">
            <div>当前第 {page + 1} 页</div>
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => Math.max(0, p - 1))}
                disabled={page === 0 || loading}
                className="h-7 px-2"
              >
                <ChevronLeft className="w-3.5 h-3.5" /> 上一页
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => p + 1)}
                disabled={users.length < PAGE_SIZE || loading}
                className="h-7 px-2"
              >
                下一页 <ChevronRight className="w-3.5 h-3.5" />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
