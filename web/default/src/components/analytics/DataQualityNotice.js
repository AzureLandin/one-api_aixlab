import React from 'react';
import { Message, Icon } from 'semantic-ui-react';
import { renderNumber } from '../../helpers/render';
import './DataQualityNotice.css';

/**
 * 缓存数据可信度与未上报说明
 * 对应实施规范 §7.1，确保用户明确理解缓存统计口径：
 * - 缓存读取 Token 为输入 Token 的一部分
 * - 未上报调用的数量与不可计算原因
 * - 异常缓存数据报警
 */
export function DataQualityNotice({ summary }) {
  if (!summary) return null;

  const notReported = summary.not_reported_cache_calls || 0;
  const invalidCalls = summary.invalid_cache_calls || 0;
  const totalCalls = summary.generated_calls || 0;

  if (notReported === 0 && invalidCalls === 0) {
    return null;
  }

  return (
    <div className="data-quality-notice">
      {notReported > 0 && (
        <Message info size="tiny" className="quality-message">
          <Icon name="info circle" />
          <span>
            当前时间范围内共有 <strong>{renderNumber(notReported)}</strong> / {renderNumber(totalCalls)} 次成功调用上游模型未返回 Prompt Caching 字段，缓存覆盖率为{' '}
            <strong>{summary.cache_coverage !== null ? `${(summary.cache_coverage * 100).toFixed(1)}%` : '—'}</strong>。
          </span>
        </Message>
      )}

      {invalidCalls > 0 && (
        <Message warning size="tiny" className="quality-message">
          <Icon name="warning sign" />
          <span>
            检测到 <strong>{renderNumber(invalidCalls)}</strong> 次调用的缓存读取值超出输入总数或格式异常，已保留原始数据，请检查对应上游渠道响应格式。
          </span>
        </Message>
      )}
    </div>
  );
}

export default DataQualityNotice;
