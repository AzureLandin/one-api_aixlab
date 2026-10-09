import React, { lazy, Suspense, useContext, useEffect } from 'react';
import { Route, Routes, Navigate, useLocation } from 'react-router-dom';
import Loading from './components/Loading';
import { PrivateRoute } from './components/PrivateRoute';
import { ConsoleLayout } from './layouts/ConsoleLayout';
import { PublicLayout } from './layouts/PublicLayout';
import { AuthLayout } from './layouts/AuthLayout';
import { API, getLogo, getSystemName, showError, showNotice } from './helpers';
import { UserContext } from './context/User';
import { StatusContext } from './context/Status';

// 页面引入
import User from './pages/User';
import Channel from './pages/Channel';
import Token from './pages/Token';
import Log from './pages/Log';
import Chat from './pages/Chat';
import Dashboard from './pages/Dashboard';
import Plaza from './pages/Plaza';
import Setting from './pages/Setting';
import EditUser from './pages/User/EditUser';
import AddUser from './pages/User/AddUser';
import EditToken from './pages/Token/EditToken';
import EditChannel from './pages/Channel/EditChannel';
import NotFound from './pages/NotFound';

import RegisterForm from './components/RegisterForm';
import LoginForm from './components/LoginForm';
import PasswordResetForm from './components/PasswordResetForm';

const Home = lazy(() => import('./pages/Home'));
const About = lazy(() => import('./pages/About'));
const Usage = lazy(() => import('./pages/Usage'));

// /console/usage 兼容跳转组件：继承原 query 字符串并重定向至 /usage
function ConsoleUsageRedirect() {
  const location = useLocation();
  return <Navigate to={`/usage${location.search}`} replace />;
}

function App() {
  const [, userDispatch] = useContext(UserContext);
  const [, statusDispatch] = useContext(StatusContext);

  const loadUser = () => {
    let user = localStorage.getItem('user');
    if (user) {
      try {
        let data = JSON.parse(user);
        userDispatch({ type: 'login', payload: data });
      } catch (e) {
        console.error('Failed to parse user session', e);
      }
    }
  };

  const loadStatus = async () => {
    try {
      const res = await API.get('/api/status');
      const { success, message, data } = res.data || {};
      if (success && data) {
        localStorage.setItem('status', JSON.stringify(data));
        statusDispatch({ type: 'set', payload: data });
        localStorage.setItem('system_name', data.system_name || 'One API');
        localStorage.setItem('logo', data.logo || '/logo.png');
        localStorage.setItem('footer_html', data.footer_html || '');
        if (data.chat_link) {
          localStorage.setItem('chat_link', data.chat_link);
        } else {
          localStorage.removeItem('chat_link');
        }
        if (
          data.version !== process.env.REACT_APP_VERSION &&
          data.version !== 'v0.0.0' &&
          process.env.REACT_APP_VERSION !== ''
        ) {
          showNotice(`新版本可用：${data.version}，请使用快捷键 Shift + F5 刷新页面`);
        }
      } else {
        showError(message || '无法正常连接至服务器！');
      }
    } catch (error) {
      showError(error.message || '无法正常连接至服务器！');
    }
  };

  useEffect(() => {
    loadUser();
    loadStatus().then();
    let systemName = getSystemName();
    if (systemName) {
      document.title = systemName;
    }
    let logo = getLogo();
    if (logo) {
      let linkElement = document.querySelector("link[rel~='icon']");
      if (linkElement) {
        linkElement.href = logo;
      }
    }
  }, []);

  return (
    <Routes>
      {/* 公共页面 */}
      <Route
        path="/"
        element={
          <PublicLayout>
            <Suspense fallback={<Loading />}>
              <Home />
            </Suspense>
          </PublicLayout>
        }
      />
      <Route
        path="/about"
        element={
          <PublicLayout>
            <Suspense fallback={<Loading />}>
              <About />
            </Suspense>
          </PublicLayout>
        }
      />

      {/* 鉴权页 (AuthLayout) */}
      <Route
        path="/login"
        element={
          <AuthLayout>
            <Suspense fallback={<Loading />}>
              <LoginForm />
            </Suspense>
          </AuthLayout>
        }
      />
      <Route
        path="/register"
        element={
          <AuthLayout>
            <Suspense fallback={<Loading />}>
              <RegisterForm />
            </Suspense>
          </AuthLayout>
        }
      />
      <Route
        path="/reset"
        element={
          <AuthLayout>
            <Suspense fallback={<Loading />}>
              <PasswordResetForm />
            </Suspense>
          </AuthLayout>
        }
      />

      {/* 控制台页面 (ConsoleLayout + PrivateRoute 角色守卫) */}
      <Route
        path="/dashboard"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Dashboard />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/usage"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <Usage />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      {/* 兼容旧 UsersTable 中的深链接入口 */}
      <Route path="/console/usage" element={<ConsoleUsageRedirect />} />

      <Route
        path="/plaza"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Plaza />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/token"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Token />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/token/add"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditToken />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/token/edit/:id"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditToken />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />

      <Route
        path="/log"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Log />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/chat"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <Chat />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />

      {/* 管理员专属页面 (requiredRole = 10) */}
      <Route
        path="/channel"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <Channel />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/channel/add"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditChannel />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/channel/edit/:id"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditChannel />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/user"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <User />
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/user/add"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <AddUser />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />
      <Route
        path="/user/edit/:id"
        element={
          <PrivateRoute requiredRole={10}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditUser />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />

      {/* 个人配置 (所有登录用户) */}
      <Route
        path="/user/edit"
        element={
          <PrivateRoute requiredRole={1}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <EditUser />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />

      {/* 系统配置 (超级管理员 root, requiredRole = 100) */}
      <Route
        path="/setting"
        element={
          <PrivateRoute requiredRole={100}>
            <ConsoleLayout>
              <Suspense fallback={<Loading />}>
                <Setting />
              </Suspense>
            </ConsoleLayout>
          </PrivateRoute>
        }
      />

      <Route
        path="*"
        element={
          <PublicLayout>
            <NotFound />
          </PublicLayout>
        }
      />
    </Routes>
  );
}

export default App;
