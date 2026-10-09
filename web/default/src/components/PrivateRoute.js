import React from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { isAdmin, isRoot } from '../helpers/utils';

export function PrivateRoute({ children, requiredRole = 1 }) {
  const location = useLocation();
  const rawUser = localStorage.getItem('user');

  if (!rawUser) {
    return <Navigate to='/login' state={{ from: location }} replace />;
  }

  // 角色要求校验: 1=普通登录用户, 10=管理员, 100=超级管理员 (root)
  if (requiredRole >= 100 && !isRoot()) {
    return <Navigate to='/dashboard' replace />;
  }
  if (requiredRole >= 10 && !isAdmin()) {
    return <Navigate to='/dashboard' replace />;
  }

  return children;
}

export default PrivateRoute;
