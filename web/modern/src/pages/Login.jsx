import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Card, CardHeader, CardTitle, CardDescription, CardContent, CardFooter } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { useAuth } from '@/context/AuthContext';

// 面板（统一账号系统）登录地址，登录后会带 redirect=model_center 自动跳回本站
const PANEL_LOGIN_URL = 'https://console.aixlab.team/login?redirect=model_center';
const REDIRECT_SECONDS = 3;

export default function Login() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const [countdown, setCountdown] = useState(REDIRECT_SECONDS);

  // cookie 有效时 AuthContext 自愈会把 user 补上，直接进控制台
  useEffect(() => {
    if (user) navigate('/dashboard', { replace: true });
  }, [user, navigate]);

  // 没有登录态时倒计时跳面板登录页
  useEffect(() => {
    if (user) return undefined;
    if (countdown <= 0) {
      window.location.href = PANEL_LOGIN_URL;
      return undefined;
    }
    const timer = setTimeout(() => setCountdown((c) => c - 1), 1000);
    return () => clearTimeout(timer);
  }, [user, countdown]);

  return (
    <div className="min-h-screen bg-background flex items-center justify-center p-4">
      <Card className="w-full max-w-sm border-border/80 bg-card/60 backdrop-blur-md shadow-2xl">
        <CardHeader className="text-center pb-6">
          <div className="w-10 h-10 rounded-xl bg-primary/10 border border-primary/20 flex items-center justify-center text-primary font-bold mx-auto mb-2 text-lg">
            1
          </div>
          <CardTitle className="text-xl font-bold tracking-tight">One API 控制台</CardTitle>
          <CardDescription className="text-xs">账号统一由 AIX Lab 面板管理</CardDescription>
        </CardHeader>
        <CardContent className="text-center text-sm text-muted-foreground space-y-2">
          {user ? (
            <p>登录状态有效，正在进入控制台…</p>
          ) : (
            <>
              <p>本站不再单独维护密码，请使用面板账号登录。</p>
              <p className="text-xs">{countdown} 秒后自动跳转面板登录页</p>
            </>
          )}
        </CardContent>
        <CardFooter className="pt-2">
          <Button className="w-full font-semibold" onClick={() => { window.location.href = PANEL_LOGIN_URL; }}>
            前往面板登录
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
