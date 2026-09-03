import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  Card,
  Form,
  Icon,
  Label,
  Loader,
  Message,
  Table,
} from 'semantic-ui-react';
import { API, copy, showError, showSuccess } from '../../helpers';
import { getChannelOption } from '../../helpers/helper';
import '../Dashboard/Dashboard.css';

const formatPer1k = (quotaPer1k, displayInCurrency, quotaPerUnit) => {
  if (displayInCurrency) {
    const amount = (quotaPer1k / quotaPerUnit).toFixed(4);
    return `$${amount} / 1K`;
  }
  const points = Number.isInteger(quotaPer1k)
    ? quotaPer1k
    : Math.round(quotaPer1k * 100) / 100;
  return `${points} / 1K`;
};

const Plaza = () => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [group, setGroup] = useState('');
  const [groupRatio, setGroupRatio] = useState(1);
  const [displayInCurrency, setDisplayInCurrency] = useState(false);
  const [quotaPerUnit, setQuotaPerUnit] = useState(500000);
  const [models, setModels] = useState([]);
  const [keyword, setKeyword] = useState('');
  const [expanded, setExpanded] = useState({});

  const loadPlaza = async () => {
    setLoading(true);
    try {
      const res = await API.get('/api/plaza/models');
      const { success, message, data } = res.data || {};
      if (success && data) {
        setGroup(data.group || '');
        setGroupRatio(data.group_ratio ?? 1);
        setDisplayInCurrency(!!data.display_in_currency);
        setQuotaPerUnit(data.quota_per_unit || 500000);
        setModels(data.models || []);
      } else {
        showError(message || t('plaza.load_failed'));
      }
    } catch (error) {
      showError(error.message || t('plaza.load_failed'));
    }
    setLoading(false);
  };

  useEffect(() => {
    loadPlaza().then();
  }, []);

  const filtered = useMemo(() => {
    const q = keyword.trim().toLowerCase();
    if (!q) {
      return models;
    }
    return models.filter((item) => {
      if (item.name.toLowerCase().includes(q)) {
        return true;
      }
      return (item.channels || []).some((ch) =>
        (ch.name || '').toLowerCase().includes(q)
      );
    });
  }, [models, keyword]);

  const toggleRow = (name) => {
    setExpanded((prev) => ({ ...prev, [name]: !prev[name] }));
  };

  const copyModel = async (name) => {
    if (await copy(name)) {
      showSuccess(t('plaza.copied'));
    } else {
      showError(t('plaza.copy_failed'));
    }
  };

  const renderChannelType = (type) => {
    const option = getChannelOption(type);
    if (!option) {
      return type;
    }
    return (
      <Label size='small' color={option.color}>
        {option.text}
      </Label>
    );
  };

  return (
    <div className='dashboard-container'>
      <Card fluid className='chart-card'>
        <Card.Content>
          <Card.Header className='header'>{t('plaza.title')}</Card.Header>
          <Message info>
            {t('plaza.intro', { group, ratio: groupRatio })}
          </Message>
          <Form onSubmit={(e) => e.preventDefault()}>
            <Form.Input
              icon='search'
              placeholder={t('plaza.search_placeholder')}
              value={keyword}
              onChange={(e, { value }) => setKeyword(value)}
            />
          </Form>
          {loading ? (
            <Loader active inline='centered' />
          ) : filtered.length === 0 ? (
            <Message>{t('plaza.empty')}</Message>
          ) : (
            <Table celled compact unstackable>
              <Table.Header>
                <Table.Row>
                  <Table.HeaderCell width={1} />
                  <Table.HeaderCell>{t('plaza.columns.model')}</Table.HeaderCell>
                  <Table.HeaderCell>{t('plaza.columns.input')}</Table.HeaderCell>
                  <Table.HeaderCell>{t('plaza.columns.output')}</Table.HeaderCell>
                  <Table.HeaderCell>{t('plaza.columns.ratio')}</Table.HeaderCell>
                  <Table.HeaderCell>{t('plaza.columns.channels')}</Table.HeaderCell>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {filtered.map((item) => (
                  <React.Fragment key={item.name}>
                    <Table.Row
                      style={{ cursor: 'pointer' }}
                      onClick={() => toggleRow(item.name)}
                    >
                      <Table.Cell textAlign='center'>
                        <Icon
                          name={expanded[item.name] ? 'angle down' : 'angle right'}
                        />
                      </Table.Cell>
                      <Table.Cell>
                        <code>{item.name}</code>
                        <Button
                          icon='copy'
                          size='mini'
                          basic
                          style={{ marginLeft: 8 }}
                          onClick={(e) => {
                            e.stopPropagation();
                            copyModel(item.name);
                          }}
                        />
                        {!item.ratio_configured && (
                          <Label size='tiny' color='orange' style={{ marginLeft: 6 }}>
                            {t('plaza.unconfigured')}
                          </Label>
                        )}
                        {item.ratio_varies && (
                          <Label size='tiny' color='yellow' style={{ marginLeft: 6 }}>
                            {t('plaza.ratio_varies')}
                          </Label>
                        )}
                      </Table.Cell>
                      <Table.Cell>
                        {formatPer1k(
                          item.input_quota_per_1k,
                          displayInCurrency,
                          quotaPerUnit
                        )}
                      </Table.Cell>
                      <Table.Cell>
                        {formatPer1k(
                          item.output_quota_per_1k,
                          displayInCurrency,
                          quotaPerUnit
                        )}
                      </Table.Cell>
                      <Table.Cell>
                        {item.model_ratio} × {item.completion_ratio} × {item.group_ratio}
                      </Table.Cell>
                      <Table.Cell>{(item.channels || []).length}</Table.Cell>
                    </Table.Row>
                    {expanded[item.name] && (
                      <Table.Row>
                        <Table.Cell colSpan='6'>
                          <Table compact celled>
                            <Table.Header>
                              <Table.Row>
                                <Table.HeaderCell>
                                  {t('plaza.channel.name')}
                                </Table.HeaderCell>
                                <Table.HeaderCell>
                                  {t('plaza.channel.type')}
                                </Table.HeaderCell>
                                <Table.HeaderCell>
                                  {t('plaza.channel.status')}
                                </Table.HeaderCell>
                                <Table.HeaderCell>
                                  {t('plaza.channel.priority')}
                                </Table.HeaderCell>
                                <Table.HeaderCell>
                                  {t('plaza.columns.input')}
                                </Table.HeaderCell>
                                <Table.HeaderCell>
                                  {t('plaza.columns.output')}
                                </Table.HeaderCell>
                              </Table.Row>
                            </Table.Header>
                            <Table.Body>
                              {(item.channels || []).map((ch) => (
                                <Table.Row key={ch.id}>
                                  <Table.Cell>{ch.name}</Table.Cell>
                                  <Table.Cell>{renderChannelType(ch.type)}</Table.Cell>
                                  <Table.Cell>
                                    <Label size='small' color='green'>
                                      {t('plaza.channel.enabled')}
                                    </Label>
                                  </Table.Cell>
                                  <Table.Cell>{ch.priority}</Table.Cell>
                                  <Table.Cell>
                                    {formatPer1k(
                                      ch.input_quota_per_1k,
                                      displayInCurrency,
                                      quotaPerUnit
                                    )}
                                  </Table.Cell>
                                  <Table.Cell>
                                    {formatPer1k(
                                      ch.output_quota_per_1k,
                                      displayInCurrency,
                                      quotaPerUnit
                                    )}
                                  </Table.Cell>
                                </Table.Row>
                              ))}
                            </Table.Body>
                          </Table>
                        </Table.Cell>
                      </Table.Row>
                    )}
                  </React.Fragment>
                ))}
              </Table.Body>
            </Table>
          )}
        </Card.Content>
      </Card>
    </div>
  );
};

export default Plaza;
