# Reversal after credit, with a finality horizon

**Decision.** `CREDITED` is not terminal: confirmation depth is a risk policy, and a deep reorg can remove a transfer after crediting. The ledger therefore has a correction path that never edits history — a **reversal**, a compensating ledger entry linked to the original credit. A second, much deeper threshold, the **finality horizon**, bounds how long the system watches a credited deposit; past it, the deposit is `FINALIZED` and treated as irreversible.

If the user already spent the funds, the reversal may drive the balance negative: the account is flagged and further debits are blocked until resolved (insurance/collection are business concerns outside the system). The horizon encodes an explicit assumption: confirmation policy and delayed spendability are best-effort risk reduction, never a guarantee against reorgs.

**Rejected alternatives.**

- Making `CREDITED` terminal — violates the requirement that no credit survives its transaction being reorged out.
- Never finalizing — every deposit watched forever; unbounded operational cost.
