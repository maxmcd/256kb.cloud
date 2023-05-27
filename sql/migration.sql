CREATE TABLE build (
    id INTEGER PRIMARY KEY,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at DATETIME,
    exit_code INTEGER,
    command TEXT NOT NULL
);
