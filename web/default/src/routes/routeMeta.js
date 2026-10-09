/**
 * 集中维护前端路由元数据
 * 包括页面标题、导航分组、权限要求、所属父级导航等
 */

export const ROUTE_ROLES = {
  PUBLIC: 0,
  USER: 1,
  ADMIN: 10,
  ROOT: 100,
};

export const ROUTE_GROUPS = {
  OVERVIEW: 'overview',
  USAGE: 'usage',
  MANAGE: 'manage',
  CONFIG: 'config',
  PUBLIC: 'public',
};

export const routeMeta = {
  '/': {
    titleKey: 'home.title',
    defaultTitle: '首页',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  },
  '/dashboard': {
    titleKey: 'dashboard.title',
    defaultTitle: '总览看板',
    group: ROUTE_GROUPS.OVERVIEW,
    role: ROUTE_ROLES.USER,
  },
  '/usage': {
    titleKey: 'usage.title',
    defaultTitle: '用量分析',
    group: ROUTE_GROUPS.OVERVIEW,
    role: ROUTE_ROLES.USER,
  },
  '/plaza': {
    titleKey: 'plaza.title',
    defaultTitle: '模型广场',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
  },
  '/token': {
    titleKey: 'token.title',
    defaultTitle: '令牌管理',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
  },
  '/token/add': {
    titleKey: 'token.add.title',
    defaultTitle: '添加令牌',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
    parentPath: '/token',
  },
  '/token/edit/:id': {
    titleKey: 'token.edit.title',
    defaultTitle: '编辑令牌',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
    parentPath: '/token',
  },
  '/log': {
    titleKey: 'log.title',
    defaultTitle: '日志明细',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
  },
  '/chat': {
    titleKey: 'chat.title',
    defaultTitle: '在线对话',
    group: ROUTE_GROUPS.USAGE,
    role: ROUTE_ROLES.USER,
  },
  '/channel': {
    titleKey: 'channel.title',
    defaultTitle: '渠道管理',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
  },
  '/channel/add': {
    titleKey: 'channel.add.title',
    defaultTitle: '添加渠道',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
    parentPath: '/channel',
  },
  '/channel/edit/:id': {
    titleKey: 'channel.edit.title',
    defaultTitle: '编辑渠道',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
    parentPath: '/channel',
  },
  '/user': {
    titleKey: 'user.title',
    defaultTitle: '用户管理',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
  },
  '/user/add': {
    titleKey: 'user.add.title',
    defaultTitle: '添加用户',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
    parentPath: '/user',
  },
  '/user/edit/:id': {
    titleKey: 'user.edit.title',
    defaultTitle: '编辑用户',
    group: ROUTE_GROUPS.MANAGE,
    role: ROUTE_ROLES.ADMIN,
    parentPath: '/user',
  },
  '/user/edit': {
    titleKey: 'user.self_edit.title',
    defaultTitle: '个人信息',
    group: ROUTE_GROUPS.CONFIG,
    role: ROUTE_ROLES.USER,
  },
  '/setting': {
    titleKey: 'setting.title',
    defaultTitle: '系统设置',
    group: ROUTE_GROUPS.CONFIG,
    role: ROUTE_ROLES.ROOT,
  },
  '/about': {
    titleKey: 'about.title',
    defaultTitle: '关于系统',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  },
  '/login': {
    titleKey: 'login.title',
    defaultTitle: '用户登录',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  },
  '/register': {
    titleKey: 'register.title',
    defaultTitle: '用户注册',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  },
  '/reset': {
    titleKey: 'reset.title',
    defaultTitle: '重置密码',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  },
};

export function getRouteMeta(pathname) {
  if (routeMeta[pathname]) return routeMeta[pathname];
  if (pathname.startsWith('/channel/edit/')) return routeMeta['/channel/edit/:id'];
  if (pathname.startsWith('/token/edit/')) return routeMeta['/token/edit/:id'];
  if (pathname.startsWith('/user/edit/')) return routeMeta['/user/edit/:id'];
  return {
    defaultTitle: 'One API',
    group: ROUTE_GROUPS.PUBLIC,
    role: ROUTE_ROLES.PUBLIC,
  };
}
