import React, { createContext, useContext, useState, useEffect } from 'react';

const AuthContext = createContext(null);

export function AuthProvider({ children }) {
  const [user, setUser] = useState(() => {
    const raw = localStorage.getItem('user');
    if (!raw) return null;
    try {
      return JSON.parse(raw);
    } catch {
      return null;
    }
  });

  // 自愈：localStorage 为空但 session cookie 有效时（例如 SSO 只写了 cookie，
  // 或用户清了站点数据但 cookie 还在），从后端取回用户信息补写 localStorage。
  useEffect(() => {
    if (user) return;
    let cancelled = false;
    fetch('/api/user/self', { credentials: 'same-origin' })
      .then((res) => (res.ok ? res.json() : null))
      .then((body) => {
        if (cancelled || !body?.success || !body?.data) return;
        localStorage.setItem('user', JSON.stringify(body.data));
        setUser(body.data);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // 只在首次挂载时检查一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const login = (userData) => {
    localStorage.setItem('user', JSON.stringify(userData));
    setUser(userData);
  };

  const logout = () => {
    localStorage.removeItem('user');
    setUser(null);
  };

  return (
    <AuthContext.Provider value={{ user, login, logout, isAdmin: user?.role >= 10, isRoot: user?.role >= 100 }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  return useContext(AuthContext);
}
