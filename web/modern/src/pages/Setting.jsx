import React, { useState, useEffect } from 'react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { API } from '@/lib/api';
import { toast } from 'react-toastify';

export default function SettingPage() {
  const [smtp, setSmtp] = useState({
    SMTPServer: '',
    SMTPPort: '587',
    SMTPAccount: '',
    SMTPFrom: '',
    SMTPToken: '',
  });

  useEffect(() => {
    API.get('/api/option/').then((res) => {
      if (res.data?.success) {
        const map = {};
        res.data.data?.forEach((item) => {
          map[item.key] = item.value;
        });
        setSmtp({
          SMTPServer: map.SMTPServer || '',
          SMTPPort: map.SMTPPort || '587',
          SMTPAccount: map.SMTPAccount || '',
          SMTPFrom: map.SMTPFrom || '',
          SMTPToken: map.SMTPToken || '',
        });
      }
    });
  }, []);

  const handleSaveSMTP = async () => {
    try {
      for (const [k, v] of Object.entries(smtp)) {
        await API.put('/api/option/', { key: k, value: v });
      }
      toast.success('SMTP 设置已保存！');
    } catch {
      toast.error('保存失败');
    }
  };

  return (
    <div className="space-y-6 max-w-4xl">
      <div className="border-b border-border pb-5">
        <h1 className="text-2xl font-bold tracking-tight text-foreground">系统配置</h1>
        <p className="text-sm text-muted-foreground mt-1">系统全局运行选项与邮件服务设置</p>
      </div>

      <Card className="bg-card/40 border-border/60">
        <CardHeader>
          <CardTitle className="text-base font-semibold">SMTP 邮件服务配置</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">SMTP 服务器地址</label>
              <Input
                placeholder="例如: 172.17.0.1 或 smtp.example.com"
                value={smtp.SMTPServer}
                onChange={(e) => setSmtp({ ...smtp, SMTPServer: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">端口</label>
              <Input
                placeholder="例如: 1025 或 465"
                value={smtp.SMTPPort}
                onChange={(e) => setSmtp({ ...smtp, SMTPPort: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">账号</label>
              <Input
                placeholder="SMTP 认证用户名"
                value={smtp.SMTPAccount}
                onChange={(e) => setSmtp({ ...smtp, SMTPAccount: e.target.value })}
              />
            </div>
            <div className="space-y-1.5">
              <label className="text-xs font-medium text-muted-foreground">发件人邮箱</label>
              <Input
                placeholder="noreply@example.com"
                value={smtp.SMTPFrom}
                onChange={(e) => setSmtp({ ...smtp, SMTPFrom: e.target.value })}
              />
            </div>
            <div className="space-y-1.5 md:col-span-2">
              <label className="text-xs font-medium text-muted-foreground">访问凭证 (Token / 密码)</label>
              <Input
                type="password"
                placeholder="密码或授权码"
                value={smtp.SMTPToken}
                onChange={(e) => setSmtp({ ...smtp, SMTPToken: e.target.value })}
              />
            </div>
          </div>
          <Button onClick={handleSaveSMTP} className="mt-2">保存 SMTP 设置</Button>
        </CardContent>
      </Card>
    </div>
  );
}
