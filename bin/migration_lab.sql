-- 课题组内网 LLM 路由器：一次性结构精简脚本
--
-- 说明：
--   1. 全新部署 **不需要** 执行本脚本，启动时 GORM AutoMigrate 会直接建出精简后的表结构。
--   2. 已有数据库请在停机后执行（先备份 one-api.db / 数据库快照）。
--   3. 本脚本主体面向 SQLite（课题组默认存储，SQLite 3.35+ 支持 DROP COLUMN）。
--      MySQL 版本见文件末尾，按需取用。
--   4. SQLite 的 ALTER TABLE ... DROP COLUMN 一次只能删一列；若某列本就不存在会报错
--      “no such column”，可忽略该条错误继续执行下一条。

-- ---------------------------------------------------------------------------
-- 1. 兑换码表整体废弃
-- ---------------------------------------------------------------------------
DROP TABLE IF EXISTS redemptions;

-- ---------------------------------------------------------------------------
-- 2. users：删除第三方登录与邀请返利相关列
--    （GitHubId / WeChatId / LarkId / OidcId / AffCode / InviterId）
-- ---------------------------------------------------------------------------
ALTER TABLE users DROP COLUMN github_id;
ALTER TABLE users DROP COLUMN wechat_id;
ALTER TABLE users DROP COLUMN lark_id;
ALTER TABLE users DROP COLUMN oidc_id;
ALTER TABLE users DROP COLUMN aff_code;
ALTER TABLE users DROP COLUMN inviter_id;

-- ---------------------------------------------------------------------------
-- 3. channels：删除权重、旧版 other 字段与上游余额爬取列
--    （Weight / Other / Balance / BalanceUpdatedTime）
-- ---------------------------------------------------------------------------
ALTER TABLE channels DROP COLUMN weight;
ALTER TABLE channels DROP COLUMN other;
ALTER TABLE channels DROP COLUMN balance;
ALTER TABLE channels DROP COLUMN balance_updated_time;

-- ---------------------------------------------------------------------------
-- 4. options：清理已从代码中移除的配置项
-- ---------------------------------------------------------------------------
DELETE FROM options WHERE "key" IN (
                            'GitHubOAuthEnabled','GitHubClientId','GitHubClientSecret',
                            'LarkClientId','LarkClientSecret',
                            'OidcEnabled','OidcClientId','OidcClientSecret','OidcWellKnown',
                            'OidcAuthorizationEndpoint','OidcTokenEndpoint','OidcUserinfoEndpoint',
                            'WeChatAuthEnabled','WeChatServerAddress','WeChatServerToken','WeChatAccountQRCodeImageURL',
                            'TurnstileCheckEnabled','TurnstileSiteKey','TurnstileSecretKey',
                            'PasswordLoginEnabled','PasswordRegisterEnabled','EmailVerificationEnabled',
                            'TopUpLink','QuotaForInviter','QuotaForInvitee','Theme'
    );

-- ---------------------------------------------------------------------------
-- 5. 旧渠道 other 数据的人工迁移（不自动做）
-- ---------------------------------------------------------------------------
-- 历史上渠道「其他」(other) 字段被用作 Azure / 讯飞的 API Version、
-- AI Proxy Library 的 LibraryID、阿里的 Plugin 参数。现在这些值统一放在
-- 渠道配置 JSON (channels.config) 的 api_version / library_id / plugin 里。
-- 如果老库里仍有非空的 other，请在删列之前到后台编辑对应渠道，把该值填入
-- 渠道配置的 api_version / library_id / plugin 后保存，再执行上面第 3 节。
-- 内网全新部署可忽略本节。

-- ---------------------------------------------------------------------------
-- MySQL 版本（按需替换上面的 SQLite 语句）
-- ---------------------------------------------------------------------------
-- DROP TABLE IF EXISTS redemptions;
-- ALTER TABLE users
--     DROP COLUMN github_id,
--     DROP COLUMN wechat_id,
--     DROP COLUMN lark_id,
--     DROP COLUMN oidc_id,
--     DROP COLUMN aff_code,
--     DROP COLUMN inviter_id;
-- ALTER TABLE channels
--     DROP COLUMN weight,
--     DROP COLUMN other,
--     DROP COLUMN balance,
--     DROP COLUMN balance_updated_time;
-- DELETE FROM options WHERE `key` IN (
--     'GitHubOAuthEnabled','GitHubClientId','GitHubClientSecret',
--     'LarkClientId','LarkClientSecret',
--     'OidcEnabled','OidcClientId','OidcClientSecret','OidcWellKnown',
--     'OidcAuthorizationEndpoint','OidcTokenEndpoint','OidcUserinfoEndpoint',
--     'WeChatAuthEnabled','WeChatServerAddress','WeChatServerToken','WeChatAccountQRCodeImageURL',
--     'TurnstileCheckEnabled','TurnstileSiteKey','TurnstileSecretKey',
--     'PasswordLoginEnabled','PasswordRegisterEnabled','EmailVerificationEnabled',
--     'TopUpLink','QuotaForInviter','QuotaForInvitee','Theme'
-- );
