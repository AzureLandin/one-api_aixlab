import React from 'react';
import { Dropdown, Button, Icon } from 'semantic-ui-react';
import './FilterBar.css';

const RANGE_OPTIONS = [
  { key: '7', value: 7, text: '最近 7 天' },
  { key: '30', value: 30, text: '最近 30 天' },
  { key: '90', value: 90, text: '最近 90 天' },
  { key: '0', value: 0, text: '全部时间' },
];

export function FilterBar({
  days = 7,
  onChangeDays,
  onRefresh,
  loading = false,
  extra,
}) {
  return (
    <div className="app-filter-bar">
      <div className="filter-bar-left">
        <span className="filter-label">时间范围:</span>
        <Dropdown
          selection
          options={RANGE_OPTIONS}
          value={days}
          onChange={(e, { value }) => onChangeDays && onChangeDays(value)}
          className="filter-range-dropdown"
        />
        {extra}
      </div>

      <div className="filter-bar-right">
        {onRefresh && (
          <Button
            basic
            inverted
            size="small"
            onClick={onRefresh}
            loading={loading}
            disabled={loading}
            className="filter-refresh-btn"
          >
            <Icon name="sync" />
            刷新
          </Button>
        )}
      </div>
    </div>
  );
}

export default FilterBar;
