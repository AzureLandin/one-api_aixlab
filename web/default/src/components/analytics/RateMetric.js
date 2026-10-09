import React from 'react';
import MetricCard from './MetricCard';

/**
 * RateMetric 严格遵守可空指标规范（§13 验收要求）：
 * - null 或 undefined 时显示 "—"（绝不显示为 0%）
 * - 正常数值时格式化为百分比（如 85.4%）
 */
export function RateMetric({
  label,
  value,
  description,
  hint,
  status = 'default',
  icon,
}) {
  const isComputable = value !== null && value !== undefined && !isNaN(value);
  const displayValue = isComputable ? `${(value * 100).toFixed(1)}%` : '—';

  return (
    <MetricCard
      label={label}
      value={displayValue}
      description={description}
      hint={hint}
      status={status}
      icon={icon}
    />
  );
}

export default RateMetric;
