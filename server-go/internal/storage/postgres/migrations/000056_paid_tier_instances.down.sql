UPDATE tier_configs SET instances = 1, updated_at = NOW() WHERE tier IN ('STANDARD', 'ENTERPRISE');
