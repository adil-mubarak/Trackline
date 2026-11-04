CREATE TABLE project_repositories (
    project_id BIGINT UNSIGNED NOT NULL,
    github_repository_id BIGINT UNSIGNED NOT NULL,
    owner VARCHAR(255) NOT NULL,
    name VARCHAR(255) NOT NULL,
    PRIMARY KEY (project_id),
    KEY project_repositories_github_repository_id (github_repository_id),
    CONSTRAINT project_repositories_project_fk FOREIGN KEY (project_id) REFERENCES projects (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
