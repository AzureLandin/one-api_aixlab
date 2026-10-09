import React, { useState, useEffect } from 'react';
import { useNavigate, Link } from 'react-router-dom';
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { API } from '@/lib/api';
import { useAuth } from '@/context/AuthContext';
import { toast } from 'react-toastify';

export default function Login() {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [loading, setLoading] = useState(false);
  const [registerEnabled, setRegisterEnabled] = useState(false);
  const { login } = useAuth();
  const navigate = useNavigate();

  // 仅在管理员开启注册时展示注册入口，避免点进去才发现被关闭
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await API.get('/api/status');
        if (!cancelled && res.data?.success) {
          setRegisterEnabled(res.data.data?.register_enabled === true);
        }
      } catch (e) {
        // 状态不可用时静默隐藏注册入口
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const handleSubmit = async (e) => {
    e.preventDefault();
    if (!username || !password) {
      toast.warn('请输入用户名和密码');
      return;
    }
    setLoading(true);
    try {
      const res = await API.post('/api/user/login', { username, password });
      if (res.data?.success) {
        toast.success('登录成功！');
        login(res.data.data);
        navigate('/dashboard');
      } else {
        toast.error(res.data?.message || '登录失败');
      }
    } catch (e) {
      toast.error('网络连接错误');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-4">
      <Card className="w-full max-w-sm border-border/80 bg-card/60 backdrop-blur-md shadow-2xl">
        <CardHeader className="text-center pb-6">
          <div className="w-10 h-10 rounded-xl bg-primary/10 border border-primary/20 flex items-center justify-center text-primary font-bold mx-auto mb-2 text-lg">
            1
          </div>
          <CardTitle className="text-xl font-bold tracking-tight">One API 控制台</CardTitle>
          <CardDescription className="text-xs">登录以管理 API 渠道与查询用量事实层</CardDescription>
        </CardHeader>
        <form onSubmit={handleSubmit}>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">用户名</label>
              <Input
                placeholder="请输入用户名"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoFocus
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">密码</label>
              <Input
                type="password"
                placeholder="请输入密码"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
          </CardContent>
          <CardFooter className="pt-2 flex-col gap-3">
            <Button type="submit" className="w-full font-semibold" disabled={loading}>
              {loading ? '正在登录...' : '立即登录'}
            </Button>
            {registerEnabled && (
              <p className="text-xs text-muted-foreground">
                还没有账号？{' '}
                <Link to="/register" className="text-primary hover:underline">
                  注册账号
                </Link>
              </p>
            )}
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
