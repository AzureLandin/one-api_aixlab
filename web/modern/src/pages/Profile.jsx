import React, { useState } from 'react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useAuth } from '@/context/AuthContext';
import { API } from '@/lib/api';
import { toast } from 'react-toastify';

export default function ProfilePage() {
  const { user } = useAuth();
  const [password, setPassword] = useState('');

  const handleUpdate = async () => {
    if (!password) {
      toast.warn('请输入要修改的新密码');
      return;
    }
    try {
      const res = await API.put('/api/user/self', { password });
      if (res.data?.success) {
        toast.success('密码已成功修改！');
        setPassword('');
      }
    } catch {}
  };

  return (
    <div className="space-y-6 max-w-xl">
      <div className="border-b border-border pb-5">
        <h1 className="text-2xl font-bold tracking-tight text-foreground">个人资料</h1>
        <p className="text-sm text-muted-foreground mt-1">查看账号基本信息与更新登录密码</p>
      </div>

      <Card className="bg-card/40 border-border/60">
        <CardHeader>
          <CardTitle className="text-base font-semibold">账号信息</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-1">
            <span className="text-xs text-muted-foreground">当前用户名</span>
            <div className="font-semibold text-foreground text-sm">{user?.username}</div>
          </div>
          <div className="space-y-1.5 pt-2">
            <label className="text-xs font-medium text-muted-foreground">修改密码</label>
            <Input
              type="password"
              placeholder="输入新密码 (至少 8 位)"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <Button onClick={handleUpdate}>保存修改</Button>
        </CardContent>
      </Card>
    </div>
  );
}
