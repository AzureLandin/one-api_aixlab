import axios from 'axios';
import { toast } from 'react-toastify';

export const API = axios.create({
  baseURL: import.meta.env.VITE_SERVER_URL || '',
});

API.interceptors.response.use(
  (response) => response,
  (error) => {
    const msg = error.response?.data?.message || error.message || '请求服务发生异常';
    toast.error(msg);
    return Promise.reject(error);
  }
);

export function isAdmin() {
  const user = localStorage.getItem('user');
  if (!user) return false;
  try {
    const parsed = JSON.parse(user);
    return parsed.role >= 10;
  } catch {
    return false;
  }
}

export function isRoot() {
  const user = localStorage.getItem('user');
  if (!user) return false;
  try {
    const parsed = JSON.parse(user);
    return parsed.role >= 100;
  } catch {
    return false;
  }
}

export function getCurrentUser() {
  const user = localStorage.getItem('user');
  if (!user) return null;
  try {
    return JSON.parse(user);
  } catch {
    return null;
  }
}
