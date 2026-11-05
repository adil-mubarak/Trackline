CREATE TABLE oauth_clients (
    client_id VARCHAR(64) NOT NULL,
    client_name VARCHAR(128) NOT NULL,
    redirect_uri VARCHAR(512) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (client_id),
    KEY oauth_clients_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE oauth_authorization_codes (
    code_hash CHAR(64) NOT NULL,
    client_id VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    redirect_uri VARCHAR(512) NOT NULL,
    code_challenge CHAR(43) NOT NULL,
    scope VARCHAR(128) NOT NULL,
    password_reset_version BIGINT UNSIGNED NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (code_hash),
    KEY oauth_codes_expires_at (expires_at),
    KEY oauth_codes_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE oauth_grants (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    client_id VARCHAR(64) NOT NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    scope VARCHAR(128) NOT NULL,
    password_reset_version BIGINT UNSIGNED NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY oauth_grants_user_id (user_id),
    KEY oauth_grants_client_id (client_id),
    KEY oauth_grants_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE oauth_refresh_tokens (
    token_hash CHAR(64) NOT NULL,
    grant_id BIGINT UNSIGNED NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    used_at TIMESTAMP NULL DEFAULT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (token_hash),
    KEY oauth_refresh_grant_id (grant_id),
    KEY oauth_refresh_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
