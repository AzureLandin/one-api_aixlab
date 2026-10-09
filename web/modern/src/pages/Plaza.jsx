import React, { useState, useEffect } from 'react';
import { 
  Copy, 
  Check, 
  Search, 
  ChevronRight, 
  Activity, 
  Info,
  Sparkles,
  Layers,
  Zap
} from 'lucide-react';
import { Card } from '@/components/ui/Card';
import { Input } from '@/components/ui/Input';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { API } from '@/lib/api';
import { copyToClipboard } from '@/lib/utils';
import { toast } from 'react-toastify';

// 品牌或系列识别生成 Logo 图标
function getModelBrand(name = '') {
  const lower = name.toLowerCase();
  if (lower.includes('claude')) {
    return {
      brand: 'Anthropic',
      color: '#d97706', // 棕橙色
      bg: 'rgba(217, 119, 6, 0.12)',
      border: 'rgba(217, 119, 6, 0.25)',
      tags: ['anthropic', 'openai', '1M', '+5'],
      symbol: (
        <svg className="w-5 h-5 text-amber-500" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 3v18M3 12h18M5.6 5.6l12.8 12.8M18.4 5.6L5.6 18.4" />
        </svg>
      )
    };
  }
  if (lower.includes('gpt') || lower.includes('o1') || lower.includes('o3') || lower.includes('openai')) {
    return {
      brand: 'OpenAI',
      color: '#10b981', // 翡翠绿
      bg: 'rgba(16, 185, 129, 0.12)',
      border: 'rgba(16, 185, 129, 0.25)',
      tags: ['openai', 'azure', '128k', '+2'],
      symbol: (
        <svg className="w-5 h-5 text-emerald-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M12 2a10 10 0 1 0 10 10H12V2z" />
          <path d="M12 12 2.1 10a10 10 0 0 0 9.9 12V12z" />
        </svg>
      )
    };
  }
  if (lower.includes('mimo') || lower.includes('xiaomi')) {
    return {
      brand: 'Xiaomi MiMo',
      color: '#f97316',
      bg: 'rgba(249, 115, 22, 0.12)',
      border: 'rgba(249, 115, 22, 0.25)',
      tags: ['openai', 'responses', 'anthropic', '256k'],
      symbol: (
        <svg className="w-5 h-5 text-orange-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <rect x="3" y="3" width="18" height="18" rx="4" />
          <path d="M8 12h8M12 8v8" />
        </svg>
      )
    };
  }
  if (lower.includes('deepseek')) {
    return {
      brand: 'DeepSeek',
      color: '#3b82f6',
      bg: 'rgba(59, 130, 246, 0.12)',
      border: 'rgba(59, 130, 246, 0.25)',
      tags: ['deepseek', 'openai', '64k'],
      symbol: (
        <svg className="w-5 h-5 text-blue-400" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <circle cx="12" cy="12" r="10" />
          <path d="m4.93 4.93 4.24 4.24M14.83 9.17l4.24-4.24M14.83 14.83l4.24 4.24M9.17 14.83l-4.24 4.24" />
        </svg>
      )
    };
  }
  return {
    brand: 'LLM',
    color: '#8b5cf6',
    bg: 'rgba(139, 92, 246, 0.12)',
    border: 'rgba(139, 92, 246, 0.25)',
    tags: ['general', 'openai'],
    symbol: <Sparkles className="w-5 h-5 text-purple-400" />
  };
}

export default function PlazaPage() {
  const [models, setModels] = useState([]);
  const [group, setGroup] = useState('default');
  const [keyword, setKeyword] = useState('');
  const [copiedName, setCopiedName] = useState(null);
  const [selectedModel, setSelectedModel] = useState(null);

  useEffect(() => {
    API.get('/api/plaza/models').then((res) => {
      if (res.data?.success) {
        setModels(res.data.data?.models || []);
        setGroup(res.data.data?.group || 'default');
      }
    });
  }, []);

  const handleCopy = async (name) => {
    const ok = await copyToClipboard(name);
    if (ok) {
      setCopiedName(name);
      toast.success(`已复制模型代码: ${name}`);
      setTimeout(() => setCopiedName(null), 2000);
    } else {
      toast.error('复制失败，请手动选择并复制');
    }
  };

  const filtered = models.filter((m) => m.name.toLowerCase().includes(keyword.toLowerCase()));

  return (
    <div className="space-y-6">
      {/* 顶栏控制区 */}
      <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between gap-4 border-b border-border pb-5">
        <div>
          <h1 className="text-2xl font-bold tracking-tight text-foreground">模型广场</h1>
          <p className="text-sm text-muted-foreground mt-1">
            当前服务分组: <span className="text-foreground font-mono font-medium px-2 py-0.5 rounded bg-muted/60">{group}</span>，展示可直接调用的模型及服务通道
          </p>
        </div>
        <div className="relative w-full sm:w-72">
          <Input
            placeholder="搜索模型名称或厂商..."
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            className="pl-9 text-xs bg-card/60"
          />
          <Search className="w-4 h-4 text-muted-foreground absolute left-3 top-2.5" />
        </div>
      </div>

      {/* 模型卡片网格布局（完全对齐设计图） */}
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-5">
        {filtered.map((model) => {
          const brandMeta = getModelBrand(model.name);
          const channelCount = model.channels?.length || 0;
          const isCopied = copiedName === model.name;

          return (
            <div
              key={model.name}
              className="group relative rounded-2xl border border-border/80 bg-[#161618] hover:border-zinc-600 transition-all duration-200 p-5 flex flex-col justify-between shadow-sm hover:shadow-md"
            >
              {/* 卡片头部：图标 + 标题 + 详情/复制操作 */}
              <div>
                <div className="flex items-start justify-between gap-3 mb-3">
                  <div className="flex items-start gap-3 min-w-0">
                    {/* 左侧圆形发光 Logo 容器 */}
                    <div 
                      className="w-10 h-10 rounded-full flex items-center justify-center shrink-0 border"
                      style={{ backgroundColor: brandMeta.bg, borderColor: brandMeta.border }}
                    >
                      {brandMeta.symbol}
                    </div>

                    {/* 模型名称 */}
                    <div className="min-w-0">
                      <h3 className="font-mono text-[15px] font-bold text-zinc-100 truncate tracking-tight" title={model.name}>
                        {model.name}
                      </h3>
                      {/* 事实层提示规格 */}
                      <div className="text-[11px] text-zinc-400 mt-0.5 flex items-center gap-2">
                        <span>支持聊天 / 补全</span>
                        <span>·</span>
                        <span className="text-emerald-400 font-mono">已就绪</span>
                      </div>
                    </div>
                  </div>

                  {/* 右侧动作按钮组：详情 > 与 复制按钮 */}
                  <div className="flex items-center gap-1.5 shrink-0">
                    <button
                      onClick={() => setSelectedModel(model)}
                      className="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-xs text-zinc-300 bg-zinc-800/80 hover:bg-zinc-700 hover:text-white transition-colors border border-zinc-700/60"
                    >
                      <span>详情</span>
                      <ChevronRight className="w-3 h-3" />
                    </button>
                    <button
                      onClick={() => handleCopy(model.name)}
                      title="复制模型代码"
                      className="w-7 h-7 rounded-full flex items-center justify-center text-zinc-400 hover:text-white bg-zinc-800/80 hover:bg-zinc-700 transition-colors border border-zinc-700/60"
                    >
                      {isCopied ? <Check className="w-3.5 h-3.5 text-emerald-400" /> : <Copy className="w-3.5 h-3.5" />}
                    </button>
                  </div>
                </div>

                {/* 描述区 */}
                <div className="text-xs text-zinc-500 my-4 min-h-[18px]">
                  {brandMeta.brand ? `${brandMeta.brand} 系列多模态通用语言大模型，支持上下文感知` : '暂无详细描述。'}
                </div>
              </div>

              {/* 卡片底部信息栏 */}
              <div className="pt-3 border-t border-zinc-800/80 flex items-end justify-between text-xs">
                {/* 分组与协议 Tag */}
                <div className="space-y-1.5">
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-zinc-300 text-xs">{group}</span>
                    <span className="text-sky-400 font-medium text-[11px]">按量路由</span>
                  </div>
                  <div className="flex items-center gap-1.5 text-[10px] text-zinc-500 font-mono">
                    {brandMeta.tags.map((tag, idx) => (
                      <span key={idx} className="hover:text-zinc-400 transition-colors">
                        {tag}
                      </span>
                    ))}
                  </div>
                </div>

                {/* 右侧延迟 / 吞吐 / 状态柱状图指标 */}
                <div className="flex items-end gap-3 text-right">
                  <div>
                    <div className="text-[10px] text-zinc-500">延迟</div>
                    <div className="font-mono text-zinc-300 text-xs mt-0.5">
                      {model.channels?.[0]?.response_time ? `${(model.channels[0].response_time / 1000).toFixed(1)}s` : '0.8s'}
                    </div>
                  </div>
                  <div>
                    <div className="text-[10px] text-zinc-500">渠道数</div>
                    <div className="font-mono text-zinc-300 text-xs mt-0.5">{channelCount} 个</div>
                  </div>
                  <div>
                    <div className="text-[10px] text-zinc-500">状态</div>
                    {/* 状态 3 柱指示器 */}
                    <div className="flex items-end gap-0.5 h-3.5 mt-1 justify-end">
                      <span className="w-0.5 h-1.5 rounded-full bg-emerald-400 inline-block animate-pulse"></span>
                      <span className="w-0.5 h-2.5 rounded-full bg-emerald-400 inline-block"></span>
                      <span className="w-0.5 h-3.5 rounded-full bg-emerald-400 inline-block"></span>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          );
        })}
      </div>

      {/* 详情抽屉/模态框 */}
      {selectedModel && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70 backdrop-blur-sm animate-in fade-in">
          <div className="bg-[#18181b] border border-zinc-700/80 rounded-2xl max-w-lg w-full p-6 shadow-2xl space-y-4">
            <div className="flex items-center justify-between border-b border-zinc-800 pb-3">
              <div className="flex items-center gap-2.5">
                <div className="w-3 h-3 rounded-full bg-emerald-400"></div>
                <h3 className="font-mono font-bold text-zinc-100">{selectedModel.name}</h3>
              </div>
              <Button variant="ghost" size="sm" onClick={() => setSelectedModel(null)} className="h-7 px-2 text-xs">
                关闭
              </Button>
            </div>

            <div className="space-y-3 text-xs">
              <div className="text-zinc-400">
                该模型目前在 <span className="font-semibold text-zinc-200">{group}</span> 分组中配置，共由以下渠道承接提供服务：
              </div>
              <div className="divide-y divide-zinc-800 border border-zinc-800 rounded-xl overflow-hidden bg-zinc-900/50">
                {selectedModel.channels?.map((ch) => (
                  <div key={ch.id} className="p-3 flex items-center justify-between">
                    <div>
                      <div className="font-medium text-zinc-200">{ch.name}</div>
                      <div className="text-[10px] text-zinc-500 font-mono mt-0.5">渠道 ID: {ch.id} · 优先级: {ch.priority}</div>
                    </div>
                    <Badge variant={ch.status === 1 ? 'success' : 'destructive'} className="text-[10px]">
                      {ch.status === 1 ? '已启用' : '禁用'}
                    </Badge>
                  </div>
                ))}
              </div>
            </div>

            <div className="pt-2 flex justify-end">
              <Button onClick={() => { handleCopy(selectedModel.name); setSelectedModel(null); }} className="gap-2">
                <Copy className="w-3.5 h-3.5" /> 复制并使用
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
