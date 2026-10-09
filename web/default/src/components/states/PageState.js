import React from 'react';
import { Message, Button, Icon, Loader } from 'semantic-ui-react';
import './PageState.css';

export function PageState({
  type = 'empty', // 'loading', 'empty', 'error', 'forbidden'
  title,
  message,
  onRetry,
  onAction,
  actionText,
}) {
  if (type === 'loading') {
    return (
      <div className="app-page-state">
        <Loader active inline="centered" inverted content={title || '加载中...'} />
      </div>
    );
  }

  const configs = {
    empty: {
      icon: 'inbox',
      defaultTitle: '暂无数据',
      defaultMessage: '当前筛选条件或时间范围内没有找到相关记录',
    },
    error: {
      icon: 'warning sign',
      defaultTitle: '请求发生错误',
      defaultMessage: '网络通信中断或服务暂时不可用，请稍后重试',
    },
    forbidden: {
      icon: 'lock',
      defaultTitle: '无权访问',
      defaultMessage: '您当前的账号角色没有查看该资源的权限',
    },
  };

  const current = configs[type] || configs.empty;

  return (
    <div className={`app-page-state state-${type}`}>
      <div className="page-state-card">
        <Icon name={current.icon} size="huge" className="page-state-icon" />
        <h2 className="page-state-title">{title || current.defaultTitle}</h2>
        <p className="page-state-desc">{message || current.defaultMessage}</p>
        <div className="page-state-actions">
          {onRetry && (
            <Button secondary onClick={onRetry}>
              <Icon name="refresh" /> 重新加载
            </Button>
          )}
          {onAction && actionText && (
            <Button primary onClick={onAction}>
              {actionText}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

export default PageState;
