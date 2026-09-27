UPDATE tier_configs SET backup_enabled = FALSE, updated_at = NOW() WHERE tier = 'FREE';
