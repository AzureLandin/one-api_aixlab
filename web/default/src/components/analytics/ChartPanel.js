import React from 'react';
import { Card, Loader, Message, Button, Icon } from 'semantic-ui-react';
import './ChartPanel.css';

export function ChartPanel({
  title,
  subtitle,
  extra,
  loading = false,
  error = false,
  empty = false,
  onRetry,
  children,
}) {
  return (
    <Card className="app-chart-panel" fluid>
      <Card.Content>
        <div className="chart-panel-header">
          <div className="chart-panel-title-group">
            <h3 className="chart-panel-title">{title}</h3>
            {subtitle && <p className="chart-panel-subtitle">{subtitle}</p>}
          </div>
          {extra && <div className="chart-panel-extra">{extra}</div>}
        </div>

        <div className="chart-panel-body">
          {loading ? (
            <div className="chart-panel-state">
              <Loader active inline="centered" inverted content="加载图表数据..." />
            </div>
          ) : error ? (
            <div className="chart-panel-state">
              <Message negative icon size="small">
                <Icon name="warning sign" />
                <Message.Content>
                  <Message.Header>图表数据加载失败</Message.Header>
                  <p>网络连接异常或接口响应错误</p>
                  {onRetry && (
                    <Button size="tiny" secondary onClick={onRetry}>
                      重试
                    </Button>
                  )}
                </Message.Content>
              </Message>
            </div>
          ) : empty ? (
            <div className="chart-panel-state empty-state">
              <Icon name="area graph" size="large" />
              <p>当前筛选范围内暂无数据</p>
            </div>
          ) : (
            children
          )}
        </div>
      </Card.Content>
    </Card>
  );
}

export default ChartPanel;
