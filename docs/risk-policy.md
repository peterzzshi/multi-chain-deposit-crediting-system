# Risk Policy

What a reorg can do to a deposit, the models behind the confirmation/exposure policy, and the parameter values the design uses. Vocabulary: [../CONTEXT.md](../CONTEXT.md). State machine: [state-machine.md](state-machine.md). Decisions: ADR [0002](adr/0002-reversal-after-credit-and-finality-horizon.md), [0006](adr/0006-risk-policy-layering.md).

## What a reorg can mean for a deposit

A transaction reorged out of its block may be **re-included** on the replacement branch, **become invalid** (ordering/state changed), or **never return**. Only the latter two put credited funds at risk. A reorg does not automatically return tokens to the sender — the canonical chain rolls back the transfer — and whether the platform absorbs a shortfall depends on when it made the funds spendable. That timing is a policy choice, not a chain fact.

Two failure modes need two different mechanisms: **natural reorgs** (no attacker; the daily reality on the given chains) are addressed by confirmation depth, and **adversarial reorgs** are bounded by the aggregate exposure cap. Neither replaces canonical-chain checks, idempotency, or append-only accounting.

## 1. Natural reorgs — drives $N_{\text{credit}}$

$$P(\text{depth} \geq d) = p_1 \cdot r^{\,d-1} \qquad X(N) = B_{\text{day}} \cdot p_1 \cdot r^{\,N-1}$$

$$N_{\text{natural}} = \min\{\,N : X(N) \leq \varepsilon\,\}$$

| Symbol | Meaning |
|---|---|
| $p_1$ | probability that a block is replaced at depth 1 (measured from chain history) |
| $r$ | decay factor per additional depth, $0 < r < 1$ (measured; the geometric model is memoryless, so it *underestimates* clustered reorgs — hence the empirical floor below) |
| $B_{\text{day}}$ | blocks per day (EVM: $86{,}400/12 = 7{,}200$; fast chain: $86{,}400/2 = 43{,}200$) |
| $\varepsilon$ | tolerated expected number of reorgs-past-depth-$N$ per day (business loss tolerance) |

Empirical floor on top, since real reorgs cluster:

$$N_{\text{empirical}} = \lceil m \cdot \text{deepest\_reorg\_seen} \rceil, \quad m \approx 2\text{–}5$$

Never credit below $\max(N_{\text{natural}},\, N_{\text{empirical}})$.

## 2. Adversarial reorg economics — drives the exposure cap, not the trigger

For a BFT PoS chain, the cost of reverting finality, and the attacker's payoff:

$$C_{\text{attack}} = f_{\text{crit}} \cdot S \cdot P \cdot (1 - \rho) + C_{\text{acquire}} + C_{\text{exec}} - R_{\text{hedge}}$$

$$V_{\text{attack}} = V_{\text{victims}} + V_{\text{short}} + \text{MEV} - C_{\text{attack}} \qquad \text{attack is rational iff } V_{\text{attack}} > 0$$

| Symbol | Meaning |
|---|---|
| $f_{\text{crit}}$ | Byzantine stake fraction required (≥ 1/3 to create conflicting finality; > 2/3 for stronger variants — protocol-specific) |
| $S \cdot P$ | total staked value |
| $\rho$ | fraction of slashed value the attacker recovers (hedges, circumvention) |
| $C_{\text{acquire}}$ | stake acquisition cost: market-impact premium, borrow rates — often dominates $f_{\text{crit}} \cdot S \cdot P$ |
| $C_{\text{exec}},\ R_{\text{hedge}},\ V_{\text{short}},\ \text{MEV}$ | execution cost; hedge/short value retained; MEV extracted during the attack window |
| $V_{\text{victims}}$ | **double-spent value aggregated across ALL simultaneous victims** — the attacker attacks the chain, not our deposit |

Why this model cannot be the credit trigger (ADR 0006):

1. $V_{\text{victims}}$ aggregates every exchange/bridge accepting the chain at shallow depth — our deposit being small does not make the attack unprofitable, so a per-deposit "value < attack cost" rule does not protect us.
2. The parameters ($S$, $P$, $\rho$, rental/bribe markets) are unmeasurable in real time and non-stationary — a token crash changes them faster than policy can safely track.

Only the aggregate exposure of *our own* unfinalized spendable value is fully under our control — hence the cap below. (For Nakamoto/PoW chains the same structure holds with hashrate-rental cost and catch-up probability decaying exponentially in depth; not applicable to the given chains.)

## 3. How the design combines them

$$N_{\text{credit}}(\text{tier}) = \max\big(N_{\text{natural}},\ N_{\text{empirical}},\ N_{\text{econ}}(\text{tier})\big) \quad \text{[credit trigger, per chain]}$$

$$N_{\text{finalize}} = \text{finality horizon, far deeper} \quad \text{[stop watching; terminal — ADR 0002]}$$

$$\sum (\text{unfinalized, spendable credited value}) \;\leq\; \min\!\Big(\frac{C_{\text{attack}}}{k},\ B_{\text{budget}}\Big)$$

| Symbol | Meaning |
|---|---|
| $N_{\text{econ}}(\text{tier})$ | depth implied by the adversarial model for that tier's deposit value |
| $k$ | safety factor (e.g., 10–100) absorbing parameter uncertainty |
| $B_{\text{budget}}$ | business risk budget for reorg losses |

On a chain with a genuine BFT finality signal we would credit at finality and the adversarial model collapses to monitoring; the given conditions (frequent deep reorgs on both chains) rule that out by assumption.

### Exposure-cap enforcement (runtime)

A monitor continuously computes $\sum (\text{spendable credited value of CREDITED-but-not-FINALIZED deposits})$, per asset and globally, against $E_{\max} = \min(C_{\text{attack}}/k,\ B_{\text{budget}})$:

- **Approaching the cap** (e.g., > 80%): alert.
- **At or over the cap**: new credits still post to the ledger — the ledger must reflect chain facts — but their **spendability is held** until aggregate exposure falls back below the cap. Spendability is the controllable axis; confirmation/canonicality and spendability are deliberately separate ([../CONTEXT.md](../CONTEXT.md), "Available balance").

Thresholds and dashboards are operational configuration, not design parameters.

## 4. Parameter values (every knob justified)

Deliberately few knobs; each appears in the capacity math or the state machine, so the estimation section doubles as their justification.

### Per chain

| Parameter | EVM (~12 s) | Fast (~2 s) | Reasoning |
|---|---|---|---|
| $N_{\text{credit}}$ | 12 (~2.5 min) | 150 (~5 min) | Credit trigger. Typical reorgs on such chains are 1–3 blocks; 12 blocks ≈ 4–6× margin at acceptable latency. Fast chain reorgs more often and deeper **[given]**, so scale to ~5 min wall-clock and validate against $m \times \text{deepest\_reorg\_seen}$. |
| $N_{\text{finalize}}$ | 100 (~20 min) | 1,000 (~33 min) | Finality horizon: stop watching, deposit terminal. ~8× credit depth; past it, residual risk is accepted by policy and bounded by $E_{\max}$. |
| retained window | 500 | 5,000 | **Derived, not a knob**: 2–5× $N_{\text{finalize}}$ of block hashes for rewind — negligible storage (< 5 MB). |

### Global

| Parameter | Value | Reasoning |
|---|---|---|
| $T_{\text{tier}}$ | \$50k (illustrative) | **One** tier boundary only: above it, wait $2 \times N_{\text{credit}}$ and hold spendability until $N_{\text{finalize}}$. More tiers add policy surface without changing the mechanism. |
| $E_{\max}$ | symbolic | Exposure cap $\min(C_{\text{attack}}/k,\ \text{business budget})$ — a business input, not an engineering constant. |
| $R$ | 5 min | Reconciliation interval against the custodian query API; bounds missed-webhook detection delay, sized to provider rate limits. |

Anything not listed (exact tier amount, $m$, $k$) is a policy default to be tuned from measured chain data, not a design parameter.

## Operational takeaway

Idempotent processing, durable block cursors, a rewind/replay window, canonical-chain checks, and append-only accounting are required regardless of the chosen thresholds. Risk calculations tune policy; they never replace correctness mechanisms.
