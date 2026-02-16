# Orchestration DSL (Side-Channel Directives)

You wanted a “side channel” that can accompany requests without being forwarded to providers.

Tokenhub supports:
1) **Out-of-band** directives (preferred): JSON field `orchestration`
2) **In-band** prefix directives (supported): a short directive block at start of user content, stripped before provider call.

This doc defines both.

---

## 1) Out-of-band JSON directives (preferred)

Example: planning + critique + refine

```json
{
  "request": {
    "messages": [
      {"role":"system","content":"You are a helpful assistant."},
      {"role":"user","content":"Design a storage benchmark plan ..."}
    ]
  },
  "orchestration": {
    "mode": "adversarial",
    "primary_min_weight": 6,
    "review_min_weight": 9,
    "iterations": 2,
    "return_plan_only": false
  }
}
```

### Modes

- `planning`: single strong model produces plan output
- `adversarial`: planner + critic + optional refinement loops
- `vote`: multiple candidates + judge selection
- `refine`: same model iterative refinement

### Role selection

If explicit model IDs are given, use them; otherwise satisfy weight constraints by routing.

---

## 2) In-band directive prefix (supported)

If client cannot send structured JSON, it can prefix user message with:

```
@@tokenhub
mode=adversarial
primary_min_weight=6
review_min_weight=9
iterations=2
@@end
<actual user prompt begins here>
```

Tokenhub will:
- parse directives
- remove block from forwarded prompt
- enforce policies

Rules:
- directives must be in the first 2KB of content
- unrecognized keys ignored
- invalid directives -> 400 with error details

---

## 3) Orchestration pipelines (semantic)

### 3.1 Planner + Critic loop

1) Planner model produces:
- a structured plan
- explicit assumptions
- risks

2) Critic model:
- attacks plan
- suggests improvements
- identifies missing constraints
- points out cheap wins

3) Refinement:
- planner revises plan using critique
- repeat iterations times

Return:
- final revised plan
- optionally include critique artifacts (configurable)

### 3.2 Vote mode

- Spawn N candidate models (or N calls)
- Spawn judge model to select best response
- Return judged response plus optional “why”

---

## 4) Output shaping

Directives may request:
- JSON-only output
- sections required
- include decision metadata

Example keys:
- `output_format=json|markdown`
- `include_decision=true|false`
- `return_plan_only=true|false`

---

## 5) Safety

Tokenhub must treat directives as *control-plane*, not prompt content:
- never forward directive text to providers
- never log directive block verbatim if it may contain secrets
- validate numeric bounds to avoid abuse (iterations <= 5 etc.)

