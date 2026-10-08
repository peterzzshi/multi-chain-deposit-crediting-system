# Risk policy layering: confirmation depth primary, economic exposure cap secondary

**Decision.** Crediting is triggered by confirmation depth (amount-tiered per chain), not by an attack-cost equation. Economic security math enters the design only as an aggregate **exposure cap** — total unfinalized, spendable credited value stays below `min(C_attack / k, business risk budget)` — and as input to tier boundaries.

Rationale:

1. The given chains reorg naturally without any attacker, and confirmation depth is the mechanism that addresses natural reorgs.
2. An attacker's payoff aggregates across all simultaneous victims, so a per-deposit "value < attack cost" rule does not protect us — only the aggregate cap on our own unfinalized spendable value is fully under our control.
3. Attack-cost parameters (staked value, token price, slashable fraction, bribe/rental markets) are unmeasurable in real time and non-stationary; they can bound exposure but cannot serve as a timely credit trigger.

On a chain with a real BFT finality signal we would credit at finality instead of depth; the given conditions (frequent, deep reorgs on both chains) rule that out by assumption.
