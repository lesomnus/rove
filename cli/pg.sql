-- What PostgreSQL can promise that the schema cannot say (design 9.4).
--
-- The domain layer already refuses all of this; these are the second answer,
-- for the day it has a bug. Every statement is safe to run again, so this
-- runs after every migration rather than being tracked.
--
-- The constraints are deferred to the end of the transaction because the
-- domain layer writes a change as "supersede the old row, then insert the new
-- ones", and in between the two are briefly both current.

-- In `public`, where every schema of the database finds it: an extension is
-- one per database, and a schema that is dropped -- a test's -- must not take
-- it along. Two processes migrating at once race on it, and the loser finds
-- it made.
DO $$
BEGIN
	CREATE EXTENSION IF NOT EXISTS btree_gist SCHEMA public;
EXCEPTION WHEN unique_violation THEN
	NULL;
END
$$;

DO $$
BEGIN
	-- Two blocking allocations of one exclusive resource never overlap: a
	-- room is not booked twice, and not booked while it is being repaired.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'allocation_exclusive_no_overlap') THEN
		ALTER TABLE allocation ADD CONSTRAINT allocation_exclusive_no_overlap
			EXCLUDE USING gist (resource_id WITH =, tstzrange(begins_at, ends_at, '[)') WITH &&)
			WHERE (blocking AND exclusive)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- A thing is in one place at a time, as far as is known now.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'placement_one_parent') THEN
		ALTER TABLE placement ADD CONSTRAINT placement_one_parent
			EXCLUDE USING gist (child_id WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- One owner, one manager and one custodian at a time.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'stewardship_one_per_role') THEN
		ALTER TABLE stewardship ADD CONSTRAINT stewardship_one_per_role
			EXCLUDE USING gist (asset_id WITH =, role WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- A relation between two things holds or does not, once at a time.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'link_once') THEN
		ALTER TABLE link ADD CONSTRAINT link_once
			EXCLUDE USING gist (source_id WITH =, target_id WITH =, kind WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
			WHERE (superseded_at IS NULL)
			DEFERRABLE INITIALLY DEFERRED;
	END IF;

	-- Stock is never below nothing.
	IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'stock_not_negative') THEN
		ALTER TABLE stock ADD CONSTRAINT stock_not_negative CHECK (quantity >= 0);
	END IF;
END
$$;
