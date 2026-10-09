import React from 'react';
import { Routes, Route, Navigate } from 'react-router-dom';
import { useAuth } from './context/AuthContext';

// Layouts & Pages
import ConsoleLayout from './layouts/ConsoleLayout';
import Dashboard from './pages/Dashboard';
import Usage from './pages/Usage';
import Tokens from './pages/Tokens';
import Channels from './pages/Channels';
import Logs from './pages/Logs';
import Plaza from './pages/Plaza';
import Setting from './pages/Setting';
import Users from './pages/Users';
import Profile from './pages/Profile';
import Login from './pages/Login';
import Register from './pages/Register';

function ProtectedRoute({ children, adminOnly = false, rootOnly = false }) {
  const { user, isAdmin, isRoot } = useAuth();
  if (!user) return <Navigate to="/login" replace />;
  if (rootOnly && !isRoot) return <Navigate to="/dashboard" replace />;
  if (adminOnly && !isAdmin) return <Navigate to="/dashboard" replace />;
  return <ConsoleLayout>{children}</ConsoleLayout>;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />
      <Route path="/" element={<Navigate to="/dashboard" replace />} />

      {/* 控制台路由 */}
      <Route path="/dashboard" element={<ProtectedRoute><Dashboard /></ProtectedRoute>} />
      <Route path="/usage" element={<ProtectedRoute><Usage /></ProtectedRoute>} />
      <Route path="/console/usage" element={<ProtectedRoute><Usage /></ProtectedRoute>} />
      <Route path="/token" element={<ProtectedRoute><Tokens /></ProtectedRoute>} />
      <Route path="/log" element={<ProtectedRoute><Logs /></ProtectedRoute>} />
      <Route path="/plaza" element={<ProtectedRoute><Plaza /></ProtectedRoute>} />
      <Route path="/user/edit" element={<ProtectedRoute><Profile /></ProtectedRoute>} />

      {/* 管理员专属 */}
      <Route path="/channel" element={<ProtectedRoute adminOnly><Channels /></ProtectedRoute>} />
      <Route path="/user" element={<ProtectedRoute adminOnly><Users /></ProtectedRoute>} />

      {/* 超级管理员专属 */}
      <Route path="/setting" element={<ProtectedRoute rootOnly><Setting /></ProtectedRoute>} />

      <Route path="*" element={<Navigate to="/dashboard" replace />} />
    </Routes>
  );
}
