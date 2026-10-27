ALTER TABLE squad
    ADD COLUMN max_concurrent_tasks INT NULL
    CONSTRAINT squad_max_concurrent_tasks_range CHECK (
        max_concurrent_tasks IS NULL OR (max_concurrent_tasks >= 1 AND max_concurrent_tasks <= 50)
    );
