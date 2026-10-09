import React from 'react';
import Topbar from '../components/layout/Topbar';
import './AuthLayout.css';

export function AuthLayout({ children }) {
  return (
    <div className="auth-layout">
      <Topbar onToggleMobileSidebar={() => {}} />
      <main className="auth-main">
        <div className="auth-card-container">
          {children}
        </div>
      </main>
    </div>
  );
}

export default AuthLayout;
