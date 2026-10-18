-- The extra-careful edits (quality: max, decision D-31, docs/design/spend-control-v1.md section 3a) are their own actions on the credit
-- ledger and holds: edit.first.max and edit.revision.max, with their own credits and budgets in the price list (2026-10c). The check on the
-- action names is widened to take them; no row changes, and the price lists that do not price them (2026-10) still work.

-- +goose Up

ALTER TABLE credit_ledger DROP CONSTRAINT credit_ledger_action_check;
ALTER TABLE credit_ledger ADD CONSTRAINT credit_ledger_action_check
    CHECK (action IS NULL OR action IN ('edit.first', 'edit.revision', 'edit.first.max', 'edit.revision.max', 'export.final', 'ideation.run'));

ALTER TABLE credit_holds DROP CONSTRAINT credit_holds_action_check;
ALTER TABLE credit_holds ADD CONSTRAINT credit_holds_action_check
    CHECK (action IN ('edit.first', 'edit.revision', 'edit.first.max', 'edit.revision.max', 'export.final', 'ideation.run'));

-- +goose Down

ALTER TABLE credit_holds DROP CONSTRAINT credit_holds_action_check;
ALTER TABLE credit_holds ADD CONSTRAINT credit_holds_action_check
    CHECK (action IN ('edit.first', 'edit.revision', 'export.final', 'ideation.run'));

ALTER TABLE credit_ledger DROP CONSTRAINT credit_ledger_action_check;
ALTER TABLE credit_ledger ADD CONSTRAINT credit_ledger_action_check
    CHECK (action IS NULL OR action IN ('edit.first', 'edit.revision', 'export.final', 'ideation.run'));
