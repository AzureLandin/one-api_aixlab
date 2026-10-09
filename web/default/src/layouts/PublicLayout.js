import React from 'react';
import Topbar from '../components/layout/Topbar';
import Footer from '../components/Footer';
import './PublicLayout.css';

export function PublicLayout({ children }) {
  return (
    <div className="public-layout">
      <Topbar onToggleMobileSidebar={() => {}} />
      <main className="public-main">
        <div className="public-content">
          {children}
        </div>
      </main>
      <Footer />
    </div>
  );
}

export default PublicLayout;
