CREATE TABLE oauth_identities (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    provider ENUM('github', 'google') NOT NULL,
    provider_subject VARCHAR(255) NOT NULL,
    provider_email VARCHAR(320) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY oauth_provider_subject_unique (provider, provider_subject),
    UNIQUE KEY oauth_user_provider_unique (user_id, provider),
    KEY oauth_user_id (user_id)
) ENGINE=InnoDB;
