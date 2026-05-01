-- Identity database schema.
-- Run manually: psql -U mallow -d identity -f identity_schema.sql

CREATE EXTENSION IF NOT EXISTS citext;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS users (
    id                      UUID        PRIMARY KEY DEFAULT uuidv7(),
    email                   CITEXT      NOT NULL,
    password                TEXT,
    phone_number            VARCHAR(20),
    google_id               VARCHAR(255),
    role                    VARCHAR(20)  NOT NULL DEFAULT 'user',
    status                  VARCHAR(30)  NOT NULL DEFAULT 'pending_verification',
    email_verified          BOOL        NOT NULL DEFAULT false,
    email_verified_at       TIMESTAMP,
    mfa_enabled             BOOL        NOT NULL DEFAULT false,
    mfa_secret              TEXT,
    last_login_at           TIMESTAMP,
    last_login_ip           VARCHAR(45),
    login_attempts          INT         NOT NULL DEFAULT 0,
    locked_until            TIMESTAMP,
    password_changed_at     TIMESTAMP   NOT NULL DEFAULT NOW(),
    terms_accepted          BOOL        NOT NULL DEFAULT false,
    terms_accepted_at       TIMESTAMP,
    data_sharing_consent    BOOL        NOT NULL DEFAULT false,
    analytics_consent       BOOL        NOT NULL DEFAULT true,
    marketing_consent       BOOL        NOT NULL DEFAULT false,
    linked_accounts         JSONB       NOT NULL DEFAULT '[]',
    last_active_at          TIMESTAMP   NOT NULL DEFAULT NOW(),
    created_at              TIMESTAMP   NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMP   NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_email_active   ON users(email)        WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uniq_phone_active   ON users(phone_number) WHERE deleted_at IS NULL AND phone_number IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uniq_google_id_active ON users(google_id)  WHERE deleted_at IS NULL AND google_id IS NOT NULL;
CREATE INDEX        IF NOT EXISTS idx_users_deleted   ON users(deleted_at);
CREATE INDEX        IF NOT EXISTS idx_users_active    ON users(last_active_at);
CREATE INDEX        IF NOT EXISTS idx_users_linked    ON users USING GIN(linked_accounts);

CREATE TABLE IF NOT EXISTS user_profiles (
    id                           UUID         PRIMARY KEY DEFAULT uuidv7(),
    user_id                      UUID         NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    full_name                    VARCHAR(255) NOT NULL DEFAULT '',
    display_name                 VARCHAR(100),
    date_of_birth                TIMESTAMP,
    avatar_url                   VARCHAR(512),
    occupation                   VARCHAR(100),
    industry                     VARCHAR(100),
    employer                     VARCHAR(255),
    dependents_count             INT,
    marital_status               VARCHAR(20),
    monthly_income_avg           DECIMAL(15,2),
    emergency_fund_months        DECIMAL(4,2),
    debt_to_income_ratio         DECIMAL(5,4),
    credit_score                 INT,
    income_stability             VARCHAR(20),
    risk_tolerance               VARCHAR(20)  NOT NULL DEFAULT 'moderate',
    investment_horizon           VARCHAR(20)  NOT NULL DEFAULT 'medium',
    investment_experience        VARCHAR(20)  NOT NULL DEFAULT 'beginner',
    budget_method                VARCHAR(20)  NOT NULL DEFAULT 'custom',
    alert_threshold_budget       DECIMAL(5,2),
    report_frequency             VARCHAR(20),
    currency_primary             VARCHAR(10)  NOT NULL DEFAULT 'VND',
    currency_secondary           VARCHAR(10)  NOT NULL DEFAULT 'USD',
    settings                     JSONB        NOT NULL DEFAULT '{}',
    preferred_report_day_of_month INT,
    onboarding_completed         BOOL         NOT NULL DEFAULT false,
    onboarding_completed_at      TIMESTAMP,
    primary_goal                 TEXT,
    created_at                   TIMESTAMP    NOT NULL DEFAULT NOW(),
    updated_at                   TIMESTAMP    NOT NULL DEFAULT NOW(),
    deleted_at                   TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_user_profiles_deleted ON user_profiles(deleted_at);

CREATE TABLE IF NOT EXISTS sessions (
    sid        VARCHAR(36)  PRIMARY KEY,
    user_id    UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip         VARCHAR(45),
    user_agent VARCHAR(512),
    expires_at TIMESTAMP    NOT NULL,
    created_at TIMESTAMP    NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id   ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_revoked   ON sessions(revoked_at);

CREATE TABLE IF NOT EXISTS token_blacklist (
    id         BIGSERIAL    PRIMARY KEY,
    jti        VARCHAR(36)  NOT NULL UNIQUE,
    user_id    UUID         NOT NULL,
    reason     VARCHAR(50)  NOT NULL DEFAULT 'logout',
    expires_at TIMESTAMP    NOT NULL,
    created_at TIMESTAMP    NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_token_blacklist_jti        ON token_blacklist(jti);
CREATE INDEX IF NOT EXISTS idx_token_blacklist_user_id    ON token_blacklist(user_id);
CREATE INDEX IF NOT EXISTS idx_token_blacklist_expires_at ON token_blacklist(expires_at);
