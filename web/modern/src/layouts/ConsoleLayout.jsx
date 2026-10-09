import React from 'react';
import { Link, NavLink, useNavigate } from 'react-router-dom';
import { 
  LayoutDashboard, 
  LineChart, 
  Blocks, 
  KeyRound, 
  FileText, 
  GitFork, 
  Users, 
  Settings, 
  LogOut,
  User,
  Menu,
  X
} from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { API } from '../lib/api';
import { Button } from '@/components/ui/Button';

export function ConsoleLayout({ children }) {
  const { user, logout, isAdmin, isRoot } = useAuth();
  const navigate = useNavigate();
  const [mobileMenuOpen, setMobileMenuOpen] = React.useState(false);

  const handleLogout = async () => {
    try {
      await API.get('/api/user/logout');
    } catch {}
    logout();
    navigate('/login');
  };

  const navGroups = [
    {
      title: '概览',
      items: [
        { to: '/dashboard', label: '总览看板', icon: LayoutDashboard },
        { to: '/usage', label: '用量分析', icon: LineChart },
      ],
    },
    {
      title: '使用',
      items: [
        { to: '/plaza', label: '模型广场', icon: Blocks },
        { to: '/token', label: 'API 令牌', icon: KeyRound },
        { to: '/log', label: '调用日志', icon: FileText },
      ],
    },
    ...(isAdmin ? [{
      title: '管理',
      items: [
        { to: '/channel', label: '渠道管理', icon: GitFork },
        { to: '/user', label: '用户管理', icon: Users },
      ],
    }] : []),
    {
      title: '设置',
      items: [
        { to: '/user/edit', label: '个人资料', icon: User },
        ...(isRoot ? [{ to: '/setting', label: '系统设置', icon: Settings }] : []),
      ],
    },
  ];

  return (
    <div className="min-h-screen bg-background flex flex-col md:flex-row">
      {/* 桌面端精致侧边栏 */}
      <aside className="hidden md:flex flex-col w-64 border-r border-border bg-card/40 backdrop-blur-md fixed top-0 bottom-0 z-30">
        <div className="h-14 border-b border-border flex items-center px-5 gap-3">
          <div className="w-7 h-7 rounded-lg bg-primary/10 border border-primary/20 flex items-center justify-center text-primary font-bold">
            1
          </div>
          <span className="font-semibold text-sm tracking-tight text-foreground">One API Console</span>
        </div>

        <div className="flex-1 overflow-y-auto px-3 py-4 space-y-6">
          {navGroups.map((group, idx) => (
            <div key={idx} className="space-y-1">
              <div className="px-3 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                {group.title}
              </div>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    className={({ isActive }) =>
                      `flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium transition-all ${
                        isActive
                          ? 'bg-primary/10 text-primary border border-primary/20 shadow-sm'
                          : 'text-muted-foreground hover:text-foreground hover:bg-muted/50'
                      }`
                    }
                  >
                    <Icon className="w-4 h-4" />
                    <span>{item.label}</span>
                  </NavLink>
                );
              })}
            </div>
          ))}
        </div>

        <div className="p-3 border-t border-border flex items-center justify-between">
          <div className="flex items-center gap-2 overflow-hidden px-2">
            <div className="w-7 h-7 rounded-full bg-secondary flex items-center justify-center text-xs font-medium text-foreground">
              {user?.username?.slice(0, 1)?.toUpperCase() || 'U'}
            </div>
            <div className="overflow-hidden">
              <div className="text-xs font-medium text-foreground truncate">{user?.username}</div>
              <div className="text-[10px] text-muted-foreground truncate">{isRoot ? '超级管理员' : isAdmin ? '管理员' : '普通用户'}</div>
            </div>
          </div>
          <Button variant="ghost" size="icon" onClick={handleLogout} title="退出登录" className="h-8 w-8 text-muted-foreground hover:text-destructive">
            <LogOut className="w-4 h-4" />
          </Button>
        </div>
      </aside>

      {/* 移动端顶栏 */}
      <div className="md:hidden h-14 border-b border-border bg-card/60 backdrop-blur-md flex items-center justify-between px-4 sticky top-0 z-40">
        <div className="flex items-center gap-2">
          <div className="w-6 h-6 rounded-md bg-primary/10 border border-primary/20 flex items-center justify-center text-primary font-bold text-xs">
            1
          </div>
          <span className="font-semibold text-sm">One API</span>
        </div>
        <Button variant="ghost" size="icon" onClick={() => setMobileMenuOpen(!mobileMenuOpen)}>
          {mobileMenuOpen ? <X className="w-5 h-5" /> : <Menu className="w-5 h-5" />}
        </Button>
      </div>

      {/* 移动端抽屉菜单 */}
      {mobileMenuOpen && (
        <div className="md:hidden fixed inset-x-0 top-14 bottom-0 bg-background/95 backdrop-blur-md z-30 p-4 overflow-y-auto space-y-6">
          {navGroups.map((group, idx) => (
            <div key={idx} className="space-y-1">
              <div className="px-3 text-xs font-semibold text-muted-foreground uppercase">{group.title}</div>
              {group.items.map((item) => {
                const Icon = item.icon;
                return (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    onClick={() => setMobileMenuOpen(false)}
                    className={({ isActive }) =>
                      `flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium ${
                        isActive ? 'bg-primary/10 text-primary font-semibold' : 'text-muted-foreground'
                      }`
                    }
                  >
                    <Icon className="w-4 h-4" />
                    <span>{item.label}</span>
                  </NavLink>
                );
              })}
            </div>
          ))}
          <Button variant="outline" className="w-full justify-start gap-2" onClick={handleLogout}>
            <LogOut className="w-4 h-4" /> 退出登录
          </Button>
        </div>
      )}

      {/* 主工作区 */}
      <main className="flex-1 md:ml-64 p-4 md:p-8 max-w-7xl">
        {children}
      </main>
    </div>
  );
}

export default ConsoleLayout;
