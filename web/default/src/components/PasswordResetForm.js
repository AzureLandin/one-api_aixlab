import React, { useEffect, useState } from 'react';
import {
  Button,
  Form,
  Grid,
  Header,
  Image,
  Card,
  Message,
} from 'semantic-ui-react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { API, getLogo, showError, showInfo, showSuccess } from '../helpers';

const PasswordResetForm = () => {
  const { t } = useTranslation();
  const [inputs, setInputs] = useState({
    email: '',
    token: '',
    password: '',
    password2: '',
  });
  const { email, token, password, password2 } = inputs;
  const [loading, setLoading] = useState(false);
  const [disableButton, setDisableButton] = useState(false);
  const [countdown, setCountdown] = useState(30);
  const logo = getLogo();
  let navigate = useNavigate();

  useEffect(() => {
    let countdownInterval = null;
    if (disableButton && countdown > 0) {
      countdownInterval = setInterval(() => {
        setCountdown(countdown - 1);
      }, 1000);
    } else if (countdown === 0) {
      setDisableButton(false);
      setCountdown(30);
    }
    return () => clearInterval(countdownInterval);
  }, [disableButton, countdown]);

  function handleChange(e) {
    const { name, value } = e.target;
    setInputs((inputs) => ({ ...inputs, [name]: value }));
  }

  const sendVerificationCode = async () => {
    if (!email) return;
    setDisableButton(true);
    setLoading(true);
    const res = await API.get(`/api/reset_password?email=${email}`);
    const { success, message } = res.data;
    if (success) {
      showSuccess(t('messages.success.password_reset_code'));
    } else {
      showError(message);
      setDisableButton(false);
      setCountdown(30);
    }
    setLoading(false);
  };

  async function handleSubmit() {
    if (!email || !token) {
      showInfo(t('auth.reset.input_code'));
      return;
    }
    if (password.length < 8) {
      showInfo(t('messages.error.password_length'));
      return;
    }
    if (password !== password2) {
      showInfo(t('messages.error.password_mismatch'));
      return;
    }
    setLoading(true);
    const res = await API.post(`/api/user/reset`, {
      email,
      token,
      password,
    });
    const { success, message } = res.data;
    if (success) {
      showSuccess(t('messages.success.password_reset'));
      navigate('/login');
    } else {
      showError(message);
    }
    setLoading(false);
  }

  return (
    <Grid textAlign='center' style={{ marginTop: '48px' }}>
      <Grid.Column style={{ maxWidth: 450 }}>
        <Card
          fluid
          className='chart-card'
          style={{ boxShadow: '0 1px 3px rgba(0,0,0,0.12)' }}
        >
          <Card.Content>
            <Card.Header>
              <Header
                as='h2'
                textAlign='center'
                style={{ marginBottom: '1.5em' }}
              >
                <Image src={logo} style={{ marginBottom: '10px' }} />
                <Header.Content>{t('auth.reset.title')}</Header.Content>
              </Header>
            </Card.Header>
            <Form size='large'>
              <Form.Input
                fluid
                icon='mail'
                iconPosition='left'
                placeholder={t('auth.reset.email')}
                name='email'
                type='email'
                value={email}
                onChange={handleChange}
                action={
                  <Button
                    onClick={sendVerificationCode}
                    disabled={loading || disableButton}
                  >
                    {disableButton
                      ? t('auth.register.get_code_retry', { countdown })
                      : t('auth.reset.get_code')}
                  </Button>
                }
                style={{ marginBottom: '1em' }}
              />
              <Form.Input
                fluid
                icon='key'
                iconPosition='left'
                placeholder={t('auth.reset.verification_code')}
                name='token'
                value={token}
                onChange={handleChange}
                style={{ marginBottom: '1em' }}
              />
              <Form.Input
                fluid
                icon='lock'
                iconPosition='left'
                placeholder={t('auth.reset.new_password')}
                name='password'
                type='password'
                value={password}
                onChange={handleChange}
                style={{ marginBottom: '1em' }}
              />
              <Form.Input
                fluid
                icon='lock'
                iconPosition='left'
                placeholder={t('auth.reset.confirm_password')}
                name='password2'
                type='password'
                value={password2}
                onChange={handleChange}
                style={{ marginBottom: '1.5em' }}
              />
              <Button
                color='blue'
                fluid
                size='large'
                onClick={handleSubmit}
                loading={loading}
                style={{
                  background: '#2F73FF', // 使用更现代的蓝色
                  color: 'white',
                  marginBottom: '1.5em',
                }}
              >
                {t('auth.reset.button')}
              </Button>
            </Form>
            <Message style={{ background: 'transparent', boxShadow: 'none' }}>
              <p style={{ fontSize: '0.9em', color: '#666' }}>
                {t('auth.reset.notice')}
              </p>
            </Message>
          </Card.Content>
        </Card>
      </Grid.Column>
    </Grid>
  );
};

export default PasswordResetForm;
