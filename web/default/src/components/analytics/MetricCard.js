import React from 'react';
import { Card, Icon, Popup } from 'semantic-ui-react';
import './MetricCard.css';

export function MetricCard({
  label,
  value,
  unit,
  description,
  hint,
  status = 'default', // 'default', 'success', 'warning', 'danger'
  icon,
}) {
  return (
    <Card className={`app-metric-card status-${status}`}>
      <Card.Content>
        <div className="metric-header">
          <div className="metric-label-wrapper">
            {icon && <Icon name={icon} className="metric-icon" />}
            <span className="metric-label">{label}</span>
          </div>
          {hint && (
            <Popup
              content={hint}
              trigger={<Icon name="info circle" className="metric-hint-icon" />}
              size="tiny"
              inverted
            />
          )}
        </div>
        <div className="metric-value-row">
          <span className="metric-value">{value}</span>
          {unit && <span className="metric-unit">{unit}</span>}
        </div>
        {description && <div className="metric-desc">{description}</div>}
      </Card.Content>
    </Card>
  );
}

export default MetricCard;
