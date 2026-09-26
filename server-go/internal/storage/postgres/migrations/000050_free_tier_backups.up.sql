UPDATE tier_configs SET backup_enabled = TRUE, updated_at = NOW() WHERE tier = 'FREE';
