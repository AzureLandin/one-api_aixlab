import React, { useState, useEffect, useRef } from 'react';
import { useNavigate, Link } from 'react-router-dom';
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { API } from '@/lib/api';
import { toast } from 'react-toastify';

const RESEND_SECONDS = 60;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export default function Register() {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [email, setEmail] = useState('');
  const [verificationCode, setVerificationCode] = useState('');

  const [loading, setLoading] = useState(false);
  const [sending, setSending] = useState(false);
  const [countdown, setCountdown] = useState(0);
  const [registerEnabled, setRegisterEnabled] = useState(true);
  const navigate = useNavigate();
  const timerRef = useRef(null);

  // 注册开关由后端 /api/status 决定，关闭时不显示表单，避免走死路
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await API.get('/api/status');
        if (!cancelled && res.data?.success) {
          setRegisterEnabled(res.data.data?.register_enabled !== false);
        }
      } catch (e) {
        // 状态拿不到时按可用处理，提交阶段后端仍会兜底校验
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (countdown <= 0) {
      if (timerRef.current) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
      return undefined;
    }
    timerRef.current = setInterval(() => {
      setCountdown((c) => (c <= 1 ? 0 : c - 1));
    }, 1000);
    return () => {
      if (timerRef.current) {
        clearInterval(timerRef.current);
        timerRef.current = null;
      }
    };
  }, [countdown]);

  const validateEmail = () => {
    if (!email.trim()) {
      toast.warn('请输入邮箱地址');
      return false;
    }
    if (!EMAIL_RE.test(email.trim())) {
      toast.warn('邮箱地址格式不正确');
      return false;
    }
    return true;
  };

  const handleSendCode = async () => {
    if (!validateEmail()) {
      return;
    }
    setSending(true);
    try {
      const res = await API.get('/api/verification', {
        params: { email: email.trim() },
      });
      if (res.data?.success) {
        toast.success('验证码已发送，请查收邮箱');
        setCountdown(RESEND_SECONDS);
      } else {
        toast.error(res.data?.message || '验证码发送失败');
      }
    } catch (e) {
      toast.error('验证码发送失败，请稍后重试');
    } finally {
      setSending(false);
    }
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    if (!username.trim()) {
      toast.warn('请输入用户名');
      return;
    }
    if (username.trim().length > 12) {
      toast.warn('用户名最长 12 个字符');
      return;
    }
    if (password.length < 8 || password.length > 20) {
      toast.warn('密码长度需为 8-20 位');
      return;
    }
    if (password !== confirmPassword) {
      toast.warn('两次输入的密码不一致');
      return;
    }
    if (!validateEmail()) {
      return;
    }
    if (!verificationCode.trim()) {
      toast.warn('请输入邮箱验证码');
      return;
    }

    setLoading(true);
    try {
      const res = await API.post('/api/user/register', {
        username: username.trim(),
        password,
        email: email.trim(),
        verification_code: verificationCode.trim(),
      });
      if (res.data?.success) {
        toast.success('注册成功，请登录');
        navigate('/login');
      } else {
        toast.error(res.data?.message || '注册失败');
      }
    } catch (err) {
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
          <CardTitle className="text-xl font-bold tracking-tight">注册账号</CardTitle>
          <CardDescription className="text-xs">
            创建账号后即可自助生成 API Key 接入网关
          </CardDescription>
        </CardHeader>

        {registerEnabled ? (
          <form onSubmit={handleSubmit}>
            <CardContent className="space-y-4">
              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">用户名</label>
                <Input
                  placeholder="最多 12 个字符"
                  value={username}
                  maxLength={12}
                  onChange={(e) => setUsername(e.target.value)}
                  autoFocus
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">密码</label>
                <Input
                  type="password"
                  placeholder="8-20 位"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">确认密码</label>
                <Input
                  type="password"
                  placeholder="请再次输入密码"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                />
              </div>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">邮箱</label>
                <div className="flex gap-2">
                  <Input
                    type="email"
                    placeholder="用于接收验证码"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    className="flex-1"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={handleSendCode}
                    disabled={sending || countdown > 0}
                    className="shrink-0 px-3 text-xs"
                  >
                    {sending ? '发送中...' : countdown > 0 ? `${countdown}s` : '发送验证码'}
                  </Button>
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="text-xs font-medium text-muted-foreground">邮箱验证码</label>
                <Input
                  placeholder="请输入邮件中的 6 位验证码"
                  value={verificationCode}
                  maxLength={6}
                  onChange={(e) => setVerificationCode(e.target.value)}
                />
              </div>
            </CardContent>
            <CardFooter className="pt-2 flex-col gap-3">
              <Button type="submit" className="w-full font-semibold" disabled={loading}>
                {loading ? '正在注册...' : '立即注册'}
              </Button>
              <p className="text-xs text-muted-foreground">
                已有账号？{' '}
                <Link to="/login" className="text-primary hover:underline">
                  返回登录
                </Link>
              </p>
            </CardFooter>
          </form>
        ) : (
          <CardContent className="space-y-4">
            <p className="text-sm text-muted-foreground text-center py-4">
              管理员已关闭新用户注册，请联系管理员开通账号。
            </p>
            <Link to="/login" className="block">
              <Button variant="outline" className="w-full">
                返回登录
              </Button>
            </Link>
          </CardContent>
        )}
      </Card>
    </div>
  );
}
