# RL / Bandit Router Design

Tokenhub can start with static rules, then graduate to a bandit-based router.

## Why bandits first (instead of full RL)

Your action space is “choose provider/model (and maybe a pipeline).”
Rewards are noisy and delayed but mostly immediate.
Bandits handle this with much less complexity.

## The problem

For a given request context (features), choose an action (model or pipeline) that maximizes:

utility = quality - cost_penalty - latency_penalty - failure_penalty

We can't directly measure “quality” perfectly, but we can proxy it via:
- user feedback signal (thumbs up/down)
- automated eval signals (critic model scores, unit tests for code tasks)
- rate of follow-up corrections (proxy for dissatisfaction)
- “self-critique confidence” scores (weak but useful)

## Step 1: Contextual bandit

### Features (x)
- estimated_input_tokens (bucketed)
- expected_output_tokens (bucketed)
- task_type (chat/code/planning) if known
- min_weight requirement
- latency budget
- budget USD
- provider health state
- recent rate limit state
- client “mode” (cheap/high_confidence/etc.)

### Actions (a)
- select model id
- select pipeline id (optional)

### Reward (r)
Define a bounded reward:
- r_quality in [0,1]
- r_cost = min(1, cost_usd / budget_usd)  (or normalized by typical cost)
- r_latency normalized to SLA

Example:
r = + 1.0 * r_quality
    - 0.5 * r_cost
    - 0.3 * r_latency
    - 1.0 * r_failure

Where failure is 1 if request failed.

## Step 2: Exploration policy

Use Thompson Sampling or UCB.

- Thompson Sampling is easy and performs well.
- Maintain per-action posterior over reward.

For contextual bandits:
- LinTS (linear Thompson Sampling)
- or simpler: bucket features and maintain per-bucket distributions

## Step 3: Guardrails

Even with learning:
- enforce hard constraints: context size, minimum weight, max budget
- never explore into ineligible models
- cap exploration rate for high-stakes requests (if mode says so)

## Step 4: Bootstrapping without user feedback

You can use “critic scoring”:

- run cheap model A answer
- run stronger critic model B to score answer (0..1)
- store score as proxy reward

Yes, this costs tokens; use sparingly:
- only for a sample of traffic
- only for uncertain routes

## Data storage

Persist:
- features hash/bucket
- chosen action
- outcomes: cost, latency, failure type
- proxy quality score
- user feedback if present

Use this to seed priors and evaluate improvements.

## Evaluation

Offline:
- replay logs with counterfactual evaluation techniques (IPS / doubly robust)
- compare against baseline policy

Online:
- small exploration % with canary rollout

