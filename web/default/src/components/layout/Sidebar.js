import React from 'react';
import { NavLink } from 'react-router-dom';
import { Icon } from 'semantic-ui-react';
import { useTranslation } from 'react-i18next';
import { isAdmin, isRoot } from '../../helpers/utils';
import './Sidebar.css';

export function Sidebar({ mobileOpen, onCloseMobile }) {
  const { t } = useTranslation();
  const isAdm = isAdmin();
  const isRt = isRoot();

  const navItems = [
    {
      group: '概览',
      items: [
        { to: '/dashboard', label: '总览看板', icon: 'chart bar' },
        { to: '/usage', label: '用量分析', icon: 'line graph' },
      ],
    },
    {
      group: '使用',
      items: [
        { to: '/plaza', label: '模型广场', icon: 'cubes' },
        { to: '/token', label: '令牌管理', icon: 'key' },
        { to: '/log', label: '日志明细', icon: 'book' },
        ...(localStorage.getItem('chat_link') ? [{ to: '/chat', label: '在线对话', icon: 'comments' }] : []),
      ],
    },
    ...(isAdm ? [{
      group: '管理',
      items: [
        { to: '/channel', label: '渠道管理', icon: 'sitemap' },
        { to: '/user', label: '用户管理', icon: 'users' },
      ],
    }] : []),
    {
      group: '配置',
      items: [
        { to: '/user/edit', label: '个人设置', icon: 'user' },
        ...(isRt ? [{ to: '/setting', label: '系统设置', icon: 'settings' }] : []),
      ],
    },
  ];

  return (
    <>
      {/* 移动端遮罩层 */}
      {mobileOpen && (
        <div className="sidebar-backdrop" onClick={onCloseMobile} />
      )}
      <aside className={`app-sidebar ${mobileOpen ? 'mobile-open' : ''}`}>
        <div className="sidebar-nav">
          {navItems.map((sec, idx) => (
            <div key={idx} className="nav-group">
              <div className="nav-group-title">{sec.group}</div>
              <div className="nav-group-items">
                {sec.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    onClick={onCloseMobile}
                    className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}
                  >
                    <Icon name={item.icon} className="nav-item-icon" />
                    <span className="nav-item-label">{item.label}</span>
                  </NavLink>
                ))}
              </div>
            </div>
          ))}
        </div>
      </aside>
    </>
  );
}

export default Sidebar;
