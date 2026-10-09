import React, { useState, useEffect } from 'react';
import { 
  Plus, 
  Trash2, 
  Play, 
  Clock, 
  ChevronLeft, 
  ChevronRight, 
  Search, 
  RefreshCw,
  X,
  Edit2,
  Network,
  ChevronDown
} from 'lucide-react';
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { Input } from '@/components/ui/Input';
import { API } from '@/lib/api';
import { toast } from 'react-toastify';

// 仅保留三种协议规范，不设置任何多余的预设供应商
const CHANNEL_TYPES = [
  { value: 50, label: 'OpenAI Compatible (兼容自定义 Base URL)' },
  { value: 14, label: 'Anthropic (Claude 协议)' },
  { value: 1, label: 'OpenAI Response / Chat' },
];

// ---- 出站代理 -----------------------------------------------------------

// 渠道级代理：one-api 通过该代理服务器去访问 Base URL。
const PROXY_SCHEMES = ['http://', 'https://', 'socks5://', 'socks5h://'];

function normalizeProxyURL(value) {
  const v = String(value || '').trim();
  if (!v) {
    return '';
  }
  return PROXY_SCHEMES.some((s) => v.toLowerCase().startsWith(s)) ? v : `http://${v}`;
}

function isValidProxyURL(value) {
  const v = normalizeProxyURL(value);
  if (!v) {
    return true; // 留空表示不使用代理
  }
  try {
    const u = new URL(v);
    return Boolean(u.hostname) && PROXY_SCHEMES.some((s) => v.toLowerCase().startsWith(s));
  } catch (e) {
    return false;
  }
}

// 读取渠道已保存的出站代理地址（存在 config.proxy_url）
function channelProxyURL(ch) {
  if (!ch || !ch.config) {
    return '';
  }
  try {
    const cfg = JSON.parse(ch.config);
    return cfg?.proxy_url || '';
  } catch (e) {
    return '';
  }
}

// ---- 逐模型配置 ---------------------------------------------------------

const INPUT_MODALITIES = [
  { value: 'text', label: '文本' },
  { value: 'image', label: '图像' },
  { value: 'audio', label: '音频' },
  { value: 'file', label: '文件' },
];

// 说明：思考等级（reasoning_effort）不在渠道侧配置。
// 客户端传什么就原样透传给上游，网关不注入、不改写、不校验。

const EMPTY_MODEL_CONFIG = {
  context_window: '',
  max_output_tokens: '',
  input_modalities: [],
};

function parseModelList(models) {
  return String(models || '')
    .split(',')
    .map((m) => m.trim())
    .filter(Boolean);
}

function joinModelList(list) {
  return list.join(',');
}

// A config entry counts as "not filled in" only when every field is blank;
// such entries are dropped so the provider default stays in effect.
function pruneModelConfig(mc) {
  if (!mc) {
    return null;
  }
  const pruned = {};
  const ctx = parseInt(mc.context_window, 10);
  if (Number.isFinite(ctx) && ctx > 0) {
    pruned.context_window = ctx;
  }
  const maxOut = parseInt(mc.max_output_tokens, 10);
  if (Number.isFinite(maxOut) && maxOut > 0) {
    pruned.max_output_tokens = maxOut;
  }
  if (Array.isArray(mc.input_modalities) && mc.input_modalities.length > 0) {
    pruned.input_modalities = mc.input_modalities;
  }
  return Object.keys(pruned).length > 0 ? pruned : null;
}

function countOverrides(mc) {
  return Object.keys(pruneModelConfig(mc) || {}).length;
}

export default function ChannelPage() {
  const [channels, setChannels] = useState([]);
  const [page, setPage] = useState(0);
  const [loading, setLoading] = useState(true);
  const [testingId, setTestingId] = useState(null);
  const [keyword, setKeyword] = useState('');

  // 抽屉状态 (新建 / 编辑)
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingChannel, setEditingChannel] = useState(null);
  const [formData, setFormData] = useState({
    name: '',
    type: 50, // 默认使用 OpenAI Compatible
    key: '',
    base_url: '',
    models: '',
    group: 'default',
    priority: 0,
  });

  // 渠道级出站代理：one-api 经由该代理服务器访问 Base URL
  const [proxyUrl, setProxyUrl] = useState('');

  // 模型配置：models 为模型名单（逗号分隔），modelConfigs 为逐模型覆盖项。
  // 未填写的字段不会下发，上游/模型提供商的默认值继续生效。
  const [modelConfigs, setModelConfigs] = useState({});
  const [expandedModel, setExpandedModel] = useState(null);
  const [fetchingModels, setFetchingModels] = useState(false);
  const [manualModel, setManualModel] = useState('');
  // 保留渠道 config 中的其它字段（如 api_version），保存时不能丢
  const [extraConfig, setExtraConfig] = useState({});

  const loadChannels = async (p = 0) => {
    setLoading(true);
    try {
      const res = await API.get(`/api/channel/?p=${p}`);
      if (res.data?.success) {
        setChannels(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  const searchChannels = async (kw) => {
    if (!kw.trim()) {
      loadChannels(0);
      return;
    }
    setLoading(true);
    try {
      const res = await API.get(`/api/channel/search?keyword=${encodeURIComponent(kw.trim())}`);
      if (res.data?.success) {
        setChannels(res.data.data || []);
      }
    } catch (e) {
      console.error(e);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadChannels(page);
  }, [page]);

  const handleOpenAdd = () => {
    setEditingChannel(null);
    setFormData({
      name: '',
      type: 50,
      key: '',
      base_url: '',
      models: '',
      group: 'default',
      priority: 0,
    });
    setProxyUrl('');
    setModelConfigs({});
    setExtraConfig({});
    setExpandedModel(null);
    setManualModel('');
    setDrawerOpen(true);
  };

  const handleOpenEdit = (ch) => {
    setEditingChannel(ch);
    setFormData({
      name: ch.name || '',
      type: ch.type || 50,
      key: ch.key || '',
      base_url: ch.base_url || '',
      models: ch.models || '',
      group: ch.group || 'default',
      priority: ch.priority || 0,
    });
    // 回填已保存的逐模型覆盖项与其它 config 字段
    let parsedConfig = {};
    if (ch.config) {
      try {
        parsedConfig = JSON.parse(ch.config) || {};
      } catch (e) {
        parsedConfig = {};
      }
    }
    setProxyUrl(parsedConfig.proxy_url || '');
    const restored = {};
    const savedModelConfigs = parsedConfig.model_configs || {};
    Object.keys(savedModelConfigs).forEach((name) => {
      const mc = savedModelConfigs[name] || {};
      restored[name] = {
        context_window: mc.context_window ?? '',
        max_output_tokens: mc.max_output_tokens ?? '',
        input_modalities: mc.input_modalities || [],
      };
    });
    setModelConfigs(restored);
    const { model_configs: _omit, ...rest } = parsedConfig;
    setExtraConfig(rest);
    setExpandedModel(null);
    setManualModel('');
    setDrawerOpen(true);
  };

  // 从上游拉取模型列表（未保存的渠道也能用，直接用表单里的地址与密钥）
  const handleFetchModels = async () => {
    setFetchingModels(true);
    try {
      const res = await API.post('/api/channel/fetch_models', {
        id: editingChannel?.id || 0,
        type: formData.type,
        base_url: formData.base_url.trim(),
        key: formData.key,
        proxy_url: normalizeProxyURL(proxyUrl),
      });
      const data = res.data || {};
      if (!data.success) {
        toast.error(data.message || '获取模型失败');
        return;
      }
      const fetched = data.data || [];
      const existing = parseModelList(formData.models);
      const merged = [...existing];
      let added = 0;
      fetched.forEach((name) => {
        if (!merged.includes(name)) {
          merged.push(name);
          added += 1;
        }
      });
      setFormData((prev) => ({ ...prev, models: joinModelList(merged) }));
      toast.success(`获取成功，共 ${fetched.length} 个模型，新增 ${added} 个`);
    } catch (e) {
      toast.error('获取模型失败：网络或上游不可达');
    } finally {
      setFetchingModels(false);
    }
  };

  const handleAddManualModel = () => {
    const name = manualModel.trim();
    if (!name) {
      return;
    }
    const existing = parseModelList(formData.models);
    if (existing.includes(name)) {
      toast.info('该模型已在列表中');
      return;
    }
    setFormData((prev) => ({ ...prev, models: joinModelList([...existing, name]) }));
    setManualModel('');
  };

  const handleRemoveModel = (name) => {
    const remaining = parseModelList(formData.models).filter((m) => m !== name);
    setFormData((prev) => ({ ...prev, models: joinModelList(remaining) }));
    setModelConfigs((prev) => {
      const next = { ...prev };
      delete next[name];
      return next;
    });
    if (expandedModel === name) {
      setExpandedModel(null);
    }
  };

  const updateModelConfig = (name, patch) => {
    setModelConfigs((prev) => ({
      ...prev,
      [name]: { ...EMPTY_MODEL_CONFIG, ...(prev[name] || {}), ...patch },
    }));
  };

  const toggleModelModality = (name, modality) => {
    const current = modelConfigs[name]?.input_modalities || [];
    const next = current.includes(modality)
      ? current.filter((m) => m !== modality)
      : [...current, modality];
    updateModelConfig(name, { input_modalities: next });
  };

  const handleSaveChannel = async (e) => {
    e.preventDefault();
    if (!formData.name.trim()) {
      toast.warn('请输入渠道名称');
      return;
    }
    if (!formData.key.trim() && !editingChannel) {
      toast.warn('请输入 API 密钥');
      return;
    }
    if (!isValidProxyURL(proxyUrl)) {
      toast.warn('代理地址格式不正确，示例：http://127.0.0.1:7890 或 socks5://127.0.0.1:1080');
      return;
    }
    // 逐模型配置：只保留真正填了内容的条目，空条目直接省略 => 提供商默认值生效
    const activeModels = parseModelList(formData.models);
    const prunedConfigs = {};
    activeModels.forEach((name) => {
      const pruned = pruneModelConfig(modelConfigs[name]);
      if (pruned) {
        prunedConfigs[name] = pruned;
      }
    });
    const mergedConfig = { ...extraConfig };
    if (Object.keys(prunedConfigs).length > 0) {
      mergedConfig.model_configs = prunedConfigs;
    } else {
      delete mergedConfig.model_configs;
    }
    // 留空即不使用代理，同时清掉可能残留的旧值
    const normalizedProxy = normalizeProxyURL(proxyUrl);
    if (normalizedProxy) {
      mergedConfig.proxy_url = normalizedProxy;
    } else {
      delete mergedConfig.proxy_url;
    }

    const payload = {
      ...formData,
      models: joinModelList(activeModels),
      config: JSON.stringify(mergedConfig),
      base_url: formData.base_url.trim(),
    };

    try {
      if (editingChannel) {
        const res = await API.put('/api/channel/', {
          ...payload,
          id: editingChannel.id,
        });
        if (res.data?.success) {
          toast.success('渠道修改成功！');
          setDrawerOpen(false);
          loadChannels(page);
        } else {
          toast.error(res.data?.message || '更新失败');
        }
      } else {
        const res = await API.post('/api/channel/', payload);
        if (res.data?.success) {
          toast.success('渠道添加成功！');
          setDrawerOpen(false);
          loadChannels(0);
        } else {
          toast.error(res.data?.message || '添加失败');
        }
      }
    } catch (err) {
      toast.error('网络请求错误');
    }
  };

  const handleTestChannel = async (id, modelName = '') => {
    setTestingId(id);
    try {
      const res = await API.get(`/api/channel/test/${id}${modelName ? `?model=${modelName}` : ''}`);
      if (res.data?.success) {
        toast.success(`渠道测试成功！耗时: ${res.data.time ? (res.data.time * 1000).toFixed(0) : 0}ms`);
        loadChannels(page);
      } else {
        toast.error(`测试失败: ${res.data?.message || '未知错误'}`);
      }
    } catch (e) {
      toast.error('网络请求错误或渠道配置异常');
    } finally {
      setTestingId(null);
    }
  };

  const handleDelete = async (id, name) => {
    if (!window.confirm(`确认删除渠道 [${name}] 吗？`)) return;
    try {
      const res = await API.delete(`/api/channel/${id}`);
      if (res.data?.success) {
        toast.success('渠道已删除');
        loadChannels(page);
      }
    } catch {}
  };

  const getTypeLabel = (type) => {
    switch (type) {
      case 50:
        return 'Compatible';
      case 14:
        return 'Anthropic';
      case 1:
        return 'Response / Chat';
      default:
        return 'Compatible';
    }
  };

  return (
    <div className="space-y-6">
      {/* 顶栏操作区 */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">渠道管理</h1>
          <p className="text-sm text-muted-foreground mt-1">仅支持 Response、Anthropic 与 Compatible 三类上游协议与网关连通分发</p>
        </div>
        <div className="flex items-center gap-3">
          <div className="relative w-60">
            <Input
              placeholder="搜索渠道 ID 或名称..."
              value={keyword}
              onChange={(e) => {
                setKeyword(e.target.value);
                if (!e.target.value) loadChannels(0);
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') searchChannels(keyword);
              }}
              className="pl-8 text-xs bg-card/60"
            />
            <Search className="w-3.5 h-3.5 text-muted-foreground absolute left-2.5 top-3" />
          </div>
          <Button onClick={handleOpenAdd} className="gap-2 shrink-0">
            <Plus className="w-4 h-4" /> 添加渠道
          </Button>
        </div>
      </div>

      {/* 渠道表格 */}
      <Card className="bg-card/40 border-border/60">
        <CardContent className="p-0">
          <div className="overflow-x-auto">
            <table className="w-full text-xs text-left">
              <thead className="border-b border-border text-muted-foreground bg-muted/20">
                <tr>
                  <th className="py-3 px-4">ID</th>
                  <th className="py-3 px-4">渠道名称</th>
                  <th className="py-3 px-4">协议类型</th>
                  <th className="py-3 px-4">分组</th>
                  <th className="py-3 px-4">Base URL</th>
                  <th className="py-3 px-4">支持模型</th>
                  <th className="py-3 px-4">响应时间</th>
                  <th className="py-3 px-4">状态</th>
                  <th className="py-3 px-4 text-right">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border/60">
                {channels.map((ch) => (
                  <tr key={ch.id} className="hover:bg-muted/30 transition-colors">
                    <td className="py-3 px-4 font-mono text-muted-foreground">{ch.id}</td>
                    <td className="py-3 px-4 font-semibold text-foreground">{ch.name}</td>
                    <td className="py-3 px-4">
                      <Badge variant="outline" className="font-mono text-[10px]">
                        {getTypeLabel(ch.type)}
                      </Badge>
                    </td>
                    <td className="py-3 px-4 font-mono text-muted-foreground">{ch.group || 'default'}</td>
                    <td className="py-3 px-4 font-mono text-muted-foreground">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate max-w-[180px]" title={ch.base_url || '官方默认'}>
                          {ch.base_url || '官方默认'}
                        </span>
                        {channelProxyURL(ch) && (
                          <span
                            className="inline-flex items-center gap-0.5 rounded border border-violet-500/40 bg-violet-500/10 px-1 py-0.5 text-[10px] font-sans text-violet-300 shrink-0"
                            title={`经由代理：${channelProxyURL(ch)}`}
                          >
                            <Network className="w-2.5 h-2.5" /> 代理
                          </span>
                        )}
                      </div>
                    </td>
                    <td className="py-3 px-4 font-mono text-muted-foreground truncate max-w-[200px]" title={ch.models}>{ch.models}</td>
                    <td className="py-3 px-4">
                      {ch.response_time > 0 ? (
                        <span className="flex items-center gap-1 text-emerald-400 font-mono">
                          <Clock className="w-3 h-3" /> {(ch.response_time / 1000).toFixed(2)}s
                        </span>
                      ) : (
                        <span className="text-muted-foreground">未测试</span>
                      )}
                    </td>
                    <td className="py-3 px-4">
                      <Badge variant={ch.status === 1 ? 'success' : 'destructive'}>
                        {ch.status === 1 ? '正常' : '已禁用'}
                      </Badge>
                    </td>
                    <td className="py-3 px-4 text-right space-x-1">
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => handleTestChannel(ch.id)}
                        disabled={testingId === ch.id}
                        className="h-7 px-2 text-xs"
                      >
                        <Play className={`w-3.5 h-3.5 mr-1 ${testingId === ch.id ? 'animate-spin' : ''}`} /> 测试
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleOpenEdit(ch)}
                        className="h-7 px-2 text-xs"
                      >
                        <Edit2 className="w-3.5 h-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleDelete(ch.id, ch.name)}
                        className="h-7 px-2 text-xs text-destructive hover:bg-destructive/10"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* 分页控制栏 */}
          <div className="p-3 border-t border-border flex items-center justify-between text-xs text-muted-foreground">
            <div>当前第 {page + 1} 页</div>
            <div className="flex items-center gap-1">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => Math.max(0, p - 1))}
                disabled={page === 0 || loading}
                className="h-7 px-2"
              >
                <ChevronLeft className="w-3.5 h-3.5" /> 上一页
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => setPage((p) => p + 1)}
                disabled={channels.length < 10 || loading}
                className="h-7 px-2"
              >
                下一页 <ChevronRight className="w-3.5 h-3.5" />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* 新增/编辑渠道模态窗口 */}
      {drawerOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70 backdrop-blur-sm animate-in fade-in">
          <div className="bg-[#18181b] border border-zinc-700/80 rounded-2xl max-w-xl w-full p-6 shadow-2xl space-y-4 max-h-[90vh] overflow-y-auto">
            <div className="flex items-center justify-between border-b border-zinc-800 pb-3">
              <h3 className="font-semibold text-zinc-100 text-base">
                {editingChannel ? `编辑渠道 (#${editingChannel.id})` : '添加新渠道'}
              </h3>
              <button onClick={() => setDrawerOpen(false)} className="text-zinc-400 hover:text-white">
                <X className="w-5 h-5" />
              </button>
            </div>

            <form onSubmit={handleSaveChannel} className="space-y-4 text-xs">
              <div className="space-y-1.5">
                <label className="text-zinc-300 font-medium">渠道名称</label>
                <Input
                  placeholder="例如：主线上游服务"
                  value={formData.name}
                  onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                  required
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-zinc-300 font-medium">协议类型</label>
                  <select
                    className="flex h-9 w-full rounded-md border border-input bg-zinc-900 px-3 py-1 text-xs shadow-sm text-zinc-200 focus:outline-none"
                    value={formData.type}
                    onChange={(e) => setFormData({ ...formData, type: parseInt(e.target.value, 10) })}
                  >
                    {CHANNEL_TYPES.map((t) => (
                      <option key={t.value} value={t.value}>
                        {t.label}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="space-y-1.5">
                  <label className="text-zinc-300 font-medium">路由分组</label>
                  <Input
                    placeholder="default"
                    value={formData.group}
                    onChange={(e) => setFormData({ ...formData, group: e.target.value })}
                  />
                </div>
              </div>

              {/* 上游地址 */}
              <div className="space-y-1.5">
                <label className="text-zinc-300 font-medium">Base URL (渠道上游地址)</label>
                <Input
                  placeholder="例如: https://api.example.com/v1"
                  value={formData.base_url}
                  onChange={(e) => setFormData({ ...formData, base_url: e.target.value })}
                  className="bg-zinc-900 font-mono"
                />
              </div>

              {/* 出站代理：one-api 经由该代理服务器去访问上面的 Base URL */}
              <div className="space-y-2 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
                <div className="flex items-center gap-2">
                  <Network className="w-3.5 h-3.5 text-violet-400" />
                  <span className="text-zinc-200 font-medium">出站代理服务器</span>
                  <span className="text-[10px] text-zinc-500">
                    选填。填写后 one-api 会经由该代理去访问上面的 Base URL
                  </span>
                </div>
                <Input
                  placeholder="例如: http://127.0.0.1:7890 或 socks5://user:pass@127.0.0.1:1080"
                  value={proxyUrl}
                  onChange={(e) => setProxyUrl(e.target.value)}
                  className="bg-zinc-900 font-mono"
                />
                <div className="flex items-center justify-between gap-2">
                  <span className="text-[10px] text-zinc-500">
                    支持 http / https / socks5 / socks5h；留空则直连，不使用任何代理
                  </span>
                  {proxyUrl.trim() && (
                    <span
                      className={`text-[10px] shrink-0 ${
                        isValidProxyURL(proxyUrl) ? 'text-emerald-400' : 'text-destructive'
                      }`}
                    >
                      {isValidProxyURL(proxyUrl) ? `将经由 ${normalizeProxyURL(proxyUrl)} 访问` : '地址格式不正确'}
                    </span>
                  )}
                </div>
              </div>

              <div className="space-y-1.5">
                <label className="text-zinc-300 font-medium">
                  API Key / 鉴权密钥 {editingChannel && '(留空表示不修改已有密钥)'}
                </label>
                <Input
                  type="password"
                  placeholder="sk-..."
                  value={formData.key}
                  onChange={(e) => setFormData({ ...formData, key: e.target.value })}
                />
              </div>

              {/* 模型配置：获取模型 + 逐模型覆盖项 */}
              <div className="space-y-2 rounded-lg border border-zinc-800 bg-zinc-900/40 p-3">
                <div className="flex items-center justify-between gap-2">
                  <div>
                    <span className="text-zinc-200 font-medium">模型配置</span>
                    <span className="text-[10px] text-zinc-500 ml-2">
                      共 {parseModelList(formData.models).length} 个模型；逐模型项留空即采用模型提供商默认值
                    </span>
                    <div className="text-[10px] text-zinc-500 mt-0.5">
                      思考等级（reasoning_effort）不在渠道侧配置，客户端传什么就原样透传给上游
                    </div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={handleFetchModels}
                      disabled={fetchingModels}
                      className="h-7 px-2 text-xs gap-1"
                    >
                      <RefreshCw className={`w-3.5 h-3.5 ${fetchingModels ? 'animate-spin' : ''}`} />
                      {fetchingModels ? '获取中...' : '获取模型'}
                    </Button>
                    {parseModelList(formData.models).length > 0 && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setFormData((prev) => ({ ...prev, models: '' }));
                          setModelConfigs({});
                          setExpandedModel(null);
                        }}
                        className="h-7 px-2 text-xs text-destructive hover:bg-destructive/10"
                      >
                        清空
                      </Button>
                    )}
                  </div>
                </div>

                {/* 手动补充单个模型 */}
                <div className="flex items-center gap-2">
                  <Input
                    placeholder="手动补充模型名，回车添加"
                    value={manualModel}
                    onChange={(e) => setManualModel(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        handleAddManualModel();
                      }
                    }}
                    className="h-8 text-xs bg-zinc-900 font-mono"
                  />
                  <Button type="button" variant="outline" size="sm" onClick={handleAddManualModel} className="h-8 px-2 text-xs shrink-0">
                    <Plus className="w-3.5 h-3.5" />
                  </Button>
                </div>

                {/* 模型列表 + 逐模型可展开配置 */}
                {parseModelList(formData.models).length === 0 ? (
                  <div className="text-[11px] text-zinc-500 py-3 text-center border border-dashed border-zinc-800 rounded-md">
                    还没有模型，点击「获取模型」从上游拉取，或手动输入模型名
                  </div>
                ) : (
                  <div className="space-y-1.5 max-h-72 overflow-y-auto pr-1">
                    {parseModelList(formData.models).map((name) => {
                      const mc = modelConfigs[name] || EMPTY_MODEL_CONFIG;
                      const overrideCount = countOverrides(mc);
                      const isExpanded = expandedModel === name;
                      return (
                        <div key={name} className="rounded-md border border-zinc-800 bg-zinc-950/60">
                          <div className="flex items-center justify-between gap-2 px-2 py-1.5">
                            <button
                              type="button"
                              onClick={() => setExpandedModel(isExpanded ? null : name)}
                              className="flex items-center gap-1.5 min-w-0 flex-1 text-left"
                            >
                              {isExpanded ? (
                                <ChevronDown className="w-3.5 h-3.5 text-zinc-500 shrink-0" />
                              ) : (
                                <ChevronRight className="w-3.5 h-3.5 text-zinc-500 shrink-0" />
                              )}
                              <span className="font-mono text-[11px] text-zinc-200 truncate">{name}</span>
                              {overrideCount > 0 ? (
                                <span className="shrink-0 rounded border border-emerald-500/40 bg-emerald-500/10 px-1 py-0.5 text-[10px] text-emerald-400">
                                  已配置 {overrideCount} 项
                                </span>
                              ) : (
                                <span className="shrink-0 text-[10px] text-zinc-600">默认</span>
                              )}
                            </button>
                            <button
                              type="button"
                              onClick={() => handleRemoveModel(name)}
                              className="text-zinc-500 hover:text-destructive shrink-0"
                              title="移除该模型"
                            >
                              <Trash2 className="w-3.5 h-3.5" />
                            </button>
                          </div>

                          {isExpanded && (
                            <div className="border-t border-zinc-800 px-2 py-2 space-y-2">
                              <div className="grid grid-cols-2 gap-2">
                                <div className="space-y-1">
                                  <label className="text-zinc-400 text-[11px]">上下文上限 (tokens)</label>
                                  <Input
                                    placeholder="留空采用提供商默认"
                                    inputMode="numeric"
                                    value={mc.context_window}
                                    onChange={(e) => updateModelConfig(name, { context_window: e.target.value })}
                                    className="h-8 text-xs bg-zinc-900"
                                  />
                                </div>
                                <div className="space-y-1">
                                  <label className="text-zinc-400 text-[11px]">最大输出 (tokens)</label>
                                  <Input
                                    placeholder="留空采用提供商默认"
                                    inputMode="numeric"
                                    value={mc.max_output_tokens}
                                    onChange={(e) => updateModelConfig(name, { max_output_tokens: e.target.value })}
                                    className="h-8 text-xs bg-zinc-900"
                                  />
                                </div>
                              </div>

                              <div className="space-y-1">
                                <label className="text-zinc-400 text-[11px]">
                                  输入模态（不勾选表示不限制）
                                </label>
                                <div className="flex flex-wrap gap-1.5">
                                  {INPUT_MODALITIES.map((mod) => {
                                    const active = (mc.input_modalities || []).includes(mod.value);
                                    return (
                                      <button
                                        key={mod.value}
                                        type="button"
                                        onClick={() => toggleModelModality(name, mod.value)}
                                        className={`rounded border px-2 py-0.5 text-[11px] transition-colors ${
                                          active
                                            ? 'border-sky-500/50 bg-sky-500/15 text-sky-300'
                                            : 'border-zinc-700 text-zinc-400 hover:border-zinc-600'
                                        }`}
                                      >
                                        {mod.label}
                                      </button>
                                    );
                                  })}
                                </div>
                              </div>
                            </div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>

              <div className="pt-3 border-t border-zinc-800 flex justify-end gap-2">
                <Button type="button" variant="outline" onClick={() => setDrawerOpen(false)}>
                  取消
                </Button>
                <Button type="submit">
                  {editingChannel ? '保存修改' : '确认添加'}
                </Button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
