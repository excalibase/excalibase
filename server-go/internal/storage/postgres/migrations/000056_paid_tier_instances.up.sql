-- Paid tiers run one instance per node: STANDARD 3, ENTERPRISE 5 (odd for quorum).
-- Only the seeded single instance is replaced; an admin's own count is kept.
UPDATE tier_configs SET instances = 3, updated_at = NOW() WHERE tier = 'STANDARD' AND instances = 1;
UPDATE tier_configs SET instances = 5, updated_at = NOW() WHERE tier = 'ENTERPRISE' AND instances = 1;
