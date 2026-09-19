-- The platform hosts the databases it provisions, so deployment_mode names
-- one of the pipelines it actually runs. The constraint stops a row in any
-- other mode from being written or from surviving an upgrade unnoticed:
-- such a row would be read as a managed instance and pause, backup and
-- deprovision would act on a database the platform does not run.
ALTER TABLE database_instances
    ADD CONSTRAINT database_instances_deployment_mode_check
    CHECK (deployment_mode IN ('k8s', 'docker'));
