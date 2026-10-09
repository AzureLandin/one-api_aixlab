import React from 'react';
import './PageHeader.css';

export function PageHeader({ title, description, actions, extra }) {
  return (
    <div className="app-page-header">
      <div className="page-header-main">
        <div className="page-header-text">
          <h1 className="page-header-title">{title}</h1>
          {description && <p className="page-header-desc">{description}</p>}
        </div>
        {actions && <div className="page-header-actions">{actions}</div>}
      </div>
      {extra && <div className="page-header-extra">{extra}</div>}
    </div>
  );
}

export default PageHeader;
