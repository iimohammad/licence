-- Issue #34: allow a shipped client holding only the license key to
-- list a license's device activations and release one that is not its
-- own (e.g. replace a lost device without opening the portal).
-- Opt-in per product because it widens what a leaked license key can
-- do: with it on, the key can free slots other devices hold.
ALTER TABLE products ADD COLUMN sdk_manage_activations boolean NOT NULL DEFAULT false;
