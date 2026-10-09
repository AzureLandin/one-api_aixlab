import React, { useContext } from 'react';
import { Link, useNavigate, useLocation } from 'react-router-dom';
import { Icon, Dropdown } from 'semantic-ui-react';
import { useTranslation } from 'react-i18next';
import { UserContext } from '../../context/User';
import { API, getLogo, getSystemName, showSuccess } from '../../helpers';
import { getRouteMeta } from '../../routes/routeMeta';
import './Topbar.css';

export function Topbar({ onToggleMobileSidebar }) {
  const { t, i18n } = useTranslation();
  const [userState, userDispatch] = useContext(UserContext);
  const navigate = useNavigate();
  const location = useLocation();

  const systemName = getSystemName();
  const logo = getLogo();
  const meta = getRouteMeta(location.pathname);

  async function handleLogout() {
    await API.get('/api/user/logout');
    showSuccess(t('header.logout_success') || '注销成功!');
    userDispatch({ type: 'logout' });
    localStorage.removeItem('user');
    navigate('/login');
  }

  return (
    <header className="app-topbar">
      <div className="topbar-left">
        <button 
          className="mobile-menu-trigger" 
          onClick={onToggleMobileSidebar}
          aria-label="Toggle menu"
        >
          <Icon name="bars" />
        </button>
        <Link to="/" className="topbar-brand">
          <img src={logo} alt="logo" className="topbar-logo" />
          <span className="topbar-title">{systemName}</span>
        </Link>
        <div className="topbar-breadcrumb">
          <span className="breadcrumb-separator">/</span>
          <span className="breadcrumb-current">{meta.defaultTitle}</span>
        </div>
      </div>

      <div className="topbar-right">
        {/* 语言切换 */}
        <Dropdown
          className="topbar-lang-dropdown"
          text={i18n.language === 'zh' ? '中' : 'EN'}
          pointing="top right"
        >
          <Dropdown.Menu>
            <Dropdown.Item onClick={() => i18n.changeLanguage('zh')}>简体中文</Dropdown.Item>
            <Dropdown.Item onClick={() => i18n.changeLanguage('en')}>English</Dropdown.Item>
          </Dropdown.Menu>
        </Dropdown>

        {/* 用户信息与菜单 */}
        {userState.user ? (
          <Dropdown
            className="topbar-user-dropdown"
            trigger={
              <span className="user-profile-trigger">
                <Icon name="user circle" />
                <span className="user-name">{userState.user.username}</span>
              </span>
            }
            pointing="top right"
          >
            <Dropdown.Menu>
              <Dropdown.Item as={Link} to="/user/edit" icon="user" text={t('header.profile') || '个人信息'} />
              <Dropdown.Divider />
              <Dropdown.Item onClick={handleLogout} icon="sign-out" text={t('header.logout') || '退出登录'} />
            </Dropdown.Menu>
          </Dropdown>
        ) : (
          <div className="topbar-auth-links">
            <Link to="/login" className="auth-btn login-btn">登录</Link>
            <Link to="/register" className="auth-btn register-btn">注册</Link>
          </div>
        )}
      </div>
    </header>
  );
}

export default Topbar;
