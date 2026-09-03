import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  Divider,
  Form,
  Grid,
  Header,
  Message,
} from 'semantic-ui-react';
import { API, removeTrailingSlash, showError } from '../helpers';

const SystemSetting = () => {
  const { t } = useTranslation();
  let [inputs, setInputs] = useState({
    Notice: '',
    SMTPServer: '',
    SMTPPort: '',
    SMTPAccount: '',
    SMTPFrom: '',
    SMTPToken: '',
    ServerAddress: '',
    Footer: '',
    MessagePusherAddress: '',
    MessagePusherToken: '',
    RegisterEnabled: '',
    EmailDomainRestrictionEnabled: '',
    EmailDomainWhitelist: '',
  });
  const [originInputs, setOriginInputs] = useState({});
  let [loading, setLoading] = useState(false);
  const [EmailDomainWhitelist, setEmailDomainWhitelist] = useState([]);
  const [restrictedDomainInput, setRestrictedDomainInput] = useState('');

  const getOptions = async () => {
    const res = await API.get('/api/option/');
    const { success, message, data } = res.data;
    if (success) {
      let newInputs = {};
      data.forEach((item) => {
        newInputs[item.key] = item.value;
      });
      setInputs({
        ...newInputs,
        EmailDomainWhitelist: newInputs.EmailDomainWhitelist.split(','),
      });
      setOriginInputs(newInputs);

      setEmailDomainWhitelist(
        newInputs.EmailDomainWhitelist.split(',').map((item) => {
          return { key: item, text: item, value: item };
        })
      );
    } else {
      showError(message);
    }
  };

  useEffect(() => {
    getOptions().then();
  }, []);

  const updateOption = async (key, value) => {
    setLoading(true);
    switch (key) {
      case 'EmailDomainRestrictionEnabled':
      case 'RegisterEnabled':
        value = inputs[key] === 'true' ? 'false' : 'true';
        break;
      default:
        break;
    }
    const res = await API.put('/api/option/', {
      key,
      value,
    });
    const { success, message } = res.data;
    if (success) {
      if (key === 'EmailDomainWhitelist') {
        value = value.split(',');
      }
      setInputs((inputs) => ({
        ...inputs,
        [key]: value,
      }));
    } else {
      showError(message);
    }
    setLoading(false);
  };

  const handleInputChange = async (e, { name, value }) => {
    if (
      name === 'Notice' ||
      name.startsWith('SMTP') ||
      name === 'ServerAddress' ||
      name === 'MessagePusherAddress' ||
      name === 'MessagePusherToken' ||
      name === 'EmailDomainWhitelist'
    ) {
      setInputs((inputs) => ({ ...inputs, [name]: value }));
    } else {
      await updateOption(name, value);
    }
  };

  const submitServerAddress = async () => {
    let ServerAddress = removeTrailingSlash(inputs.ServerAddress);
    await updateOption('ServerAddress', ServerAddress);
  };

  const submitSMTP = async () => {
    if (originInputs['SMTPServer'] !== inputs.SMTPServer) {
      await updateOption('SMTPServer', inputs.SMTPServer);
    }
    if (originInputs['SMTPAccount'] !== inputs.SMTPAccount) {
      await updateOption('SMTPAccount', inputs.SMTPAccount);
    }
    if (originInputs['SMTPFrom'] !== inputs.SMTPFrom) {
      await updateOption('SMTPFrom', inputs.SMTPFrom);
    }
    if (
      originInputs['SMTPPort'] !== inputs.SMTPPort &&
      inputs.SMTPPort !== ''
    ) {
      await updateOption('SMTPPort', inputs.SMTPPort);
    }
    if (
      originInputs['SMTPToken'] !== inputs.SMTPToken &&
      inputs.SMTPToken !== ''
    ) {
      await updateOption('SMTPToken', inputs.SMTPToken);
    }
  };

  const submitEmailDomainWhitelist = async () => {
    if (
      originInputs['EmailDomainWhitelist'] !==
        inputs.EmailDomainWhitelist.join(',') &&
      inputs.SMTPToken !== ''
    ) {
      await updateOption(
        'EmailDomainWhitelist',
        inputs.EmailDomainWhitelist.join(',')
      );
    }
  };

  const submitMessagePusher = async () => {
    if (originInputs['MessagePusherAddress'] !== inputs.MessagePusherAddress) {
      await updateOption(
        'MessagePusherAddress',
        removeTrailingSlash(inputs.MessagePusherAddress)
      );
    }
    if (
      originInputs['MessagePusherToken'] !== inputs.MessagePusherToken &&
      inputs.MessagePusherToken !== ''
    ) {
      await updateOption('MessagePusherToken', inputs.MessagePusherToken);
    }
  };

  const submitNewRestrictedDomain = () => {
    const localDomainList = inputs.EmailDomainWhitelist;
    if (
      restrictedDomainInput !== '' &&
      !localDomainList.includes(restrictedDomainInput)
    ) {
      setRestrictedDomainInput('');
      setInputs({
        ...inputs,
        EmailDomainWhitelist: [...localDomainList, restrictedDomainInput],
      });
      setEmailDomainWhitelist([
        ...EmailDomainWhitelist,
        {
          key: restrictedDomainInput,
          text: restrictedDomainInput,
          value: restrictedDomainInput,
        },
      ]);
    }
  };

  return (
    <Grid columns={1}>
      <Grid.Column>
        <Form loading={loading}>
          <Header as='h3'>{t('setting.system.general.title')}</Header>
          <Form.Group widths='equal'>
            <Form.Input
              label={t('setting.system.general.server_address')}
              placeholder={t(
                'setting.system.general.server_address_placeholder'
              )}
              value={inputs.ServerAddress}
              name='ServerAddress'
              onChange={handleInputChange}
            />
          </Form.Group>
          <Form.Button onClick={submitServerAddress}>
            {t('setting.system.general.buttons.update')}
          </Form.Button>
          <Divider />
          <Header as='h3'>{t('setting.system.login.title')}</Header>
          <Form.Group inline>
            <Form.Checkbox
              checked={inputs.RegisterEnabled === 'true'}
              label={t('setting.system.login.registration')}
              name='RegisterEnabled'
              onChange={handleInputChange}
            />
          </Form.Group>
          <Divider />
          <Header as='h3'>{t('setting.system.email_restriction.title')}</Header>
          <Message>{t('setting.system.email_restriction.subtitle')}</Message>
          <Form.Group inline>
            <Form.Checkbox
              checked={inputs.EmailDomainRestrictionEnabled === 'true'}
              label={t('setting.system.email_restriction.enable')}
              name='EmailDomainRestrictionEnabled'
              onChange={handleInputChange}
            />
          </Form.Group>
          <Form.Group widths={3}>
            <Form.Input
              label={t('setting.system.email_restriction.add_domain')}
              placeholder={t(
                'setting.system.email_restriction.add_domain_placeholder'
              )}
              value={restrictedDomainInput}
              onChange={(e, { value }) => {
                setRestrictedDomainInput(value);
              }}
              action={
                <Button
                  onClick={() => {
                    if (restrictedDomainInput === '') return;
                    setEmailDomainWhitelist([
                      ...EmailDomainWhitelist,
                      {
                        key: restrictedDomainInput,
                        text: restrictedDomainInput,
                        value: restrictedDomainInput,
                      },
                    ]);
                    setRestrictedDomainInput('');
                  }}
                >
                  {t('setting.system.email_restriction.buttons.fill')}
                </Button>
              }
            />
          </Form.Group>
          <Form.Dropdown
            label={t('setting.system.email_restriction.allowed_domains')}
            placeholder={t('setting.system.email_restriction.allowed_domains')}
            fluid
            multiple
            search
            selection
            allowAdditions
            value={EmailDomainWhitelist.map((item) => item.value)}
            options={EmailDomainWhitelist}
            onAddItem={(e, { value }) => {
              setEmailDomainWhitelist([
                ...EmailDomainWhitelist,
                {
                  key: value,
                  text: value,
                  value: value,
                },
              ]);
            }}
            onChange={(e, { value }) => {
              let newEmailDomainWhitelist = [];
              value.forEach((item) => {
                newEmailDomainWhitelist.push({
                  key: item,
                  text: item,
                  value: item,
                });
              });
              setEmailDomainWhitelist(newEmailDomainWhitelist);
            }}
          />
          <Form.Button onClick={submitEmailDomainWhitelist}>
            {t('setting.system.email_restriction.buttons.save')}
          </Form.Button>

          <Divider />
          <Header as='h3'>{t('setting.system.smtp.title')}</Header>
          <Message>{t('setting.system.smtp.subtitle')}</Message>
          <Form.Group widths={3}>
            <Form.Input
              label={t('setting.system.smtp.server')}
              placeholder={t('setting.system.smtp.server_placeholder')}
              name='SMTPServer'
              onChange={handleInputChange}
              value={inputs.SMTPServer}
            />
            <Form.Input
              label={t('setting.system.smtp.port')}
              placeholder={t('setting.system.smtp.port_placeholder')}
              name='SMTPPort'
              onChange={handleInputChange}
              value={inputs.SMTPPort}
            />
            <Form.Input
              label={t('setting.system.smtp.account')}
              placeholder={t('setting.system.smtp.account_placeholder')}
              name='SMTPAccount'
              onChange={handleInputChange}
              value={inputs.SMTPAccount}
            />
          </Form.Group>
          <Form.Group widths={3}>
            <Form.Input
              label={t('setting.system.smtp.from')}
              placeholder={t('setting.system.smtp.from_placeholder')}
              name='SMTPFrom'
              onChange={handleInputChange}
              value={inputs.SMTPFrom}
            />
            <Form.Input
              label={t('setting.system.smtp.token')}
              placeholder={t('setting.system.smtp.token_placeholder')}
              name='SMTPToken'
              onChange={handleInputChange}
              type='password'
              value={inputs.SMTPToken}
            />
          </Form.Group>
          <Form.Button onClick={submitSMTP}>
            {t('setting.system.smtp.buttons.save')}
          </Form.Button>
        </Form>
      </Grid.Column>
    </Grid>
  );
};

export default SystemSetting;
