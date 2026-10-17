-- credit_holds.frozen_source_id, frozen_credits (P7-01a): the part of a hold that rests on a source that expired while the hold was active.
--
-- A run that starts before a period ends and finishes after it holds credits of the allowance that expires in between. The expiry must not
-- take them, or the run would be charged again from the next allowance. So when a source expires, the credits that active holds rest on
-- are left in it ("frozen" for those holds, in the order the holds were made), the rest expires, and:
--   - settling the hold with a charge takes the frozen credits from that source first, and the remainder from the live sources;
--   - releasing the hold (or sweeping it) expires the frozen credits then, exactly (an expire row naming the source and the hold).
-- A hold counts against what is available only for the part that is not frozen: the frozen part is out of the live sources already.

-- +goose Up

ALTER TABLE credit_holds
    ADD COLUMN frozen_source_id uuid,
    ADD COLUMN frozen_credits   integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT credit_holds_frozen_check CHECK (frozen_credits >= 0 AND frozen_credits <= credits AND (frozen_credits = 0) = (frozen_source_id IS NULL)),
    ADD CONSTRAINT credit_holds_frozen_fkey FOREIGN KEY (workspace_id, frozen_source_id) REFERENCES credit_ledger (workspace_id, id);

-- +goose Down

ALTER TABLE credit_holds
    DROP CONSTRAINT credit_holds_frozen_fkey,
    DROP CONSTRAINT credit_holds_frozen_check,
    DROP COLUMN frozen_credits,
    DROP COLUMN frozen_source_id;
