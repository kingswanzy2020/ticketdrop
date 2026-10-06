# Journal

One entry per phase, written by me when the phase ends and before the next one
starts. The facts live in [DECISIONS.md](DECISIONS.md),
[TROUBLESHOOTING.md](TROUBLESHOOTING.md) and [PROOF.md](PROOF.md); this page
is what I make of them.

Each entry answers the same six questions, so that at the end I can say of any
part of the system: I built this, this was the problem, this is why I chose
this approach, this is what broke, this is what I changed, and this is what I
would do differently at a different scale.

While a phase is under way, add a dated line to its **Notes** as things happen.
A surprise, a wrong assumption, a number, a dead end. One line is enough; the
entry is written from these.

---

## Entry template

```markdown
## Phase N: <name>

Dates: <start> to <end>. Exit check: <the check from the plan, and whether it passed>.

### Notes
- <date>: <what happened>

### 1. The problem
What this phase had to solve, and what would have gone wrong without it.

### 2. What I built
In a few lines. What I wrote myself and what I was given.

### 3. Why this approach
The two or three decisions that mattered most, the alternatives I turned down,
and what each choice cost. Link the DECISIONS entries.

### 4. What broke
What failed, on purpose or not, and how I found the cause. Link the
TROUBLESHOOTING entries and the evidence.

### 5. What I changed
What the failures or the measurements made me do differently.

### 6. At a different scale
What I would do for a tenth of the load, and for a hundred times it, and what
signal would tell me it was time to change.

### Evidence
Links to check runs in PROOF.md and files in `evidence/phase-N/`.
```

---

## Phase 1: Services, local

Dates: 3 October 2026 to . Exit check: a simulated drop finishes with every
order ticketed or failed and tickets issued within capacity; killing a service
mid-run causes redelivery without duplicates; a poison message lands in the
dead-letter queue.

The service code in this phase was written for me. My part was to read it, run
it, break it and understand why it is built this way.

The record for this phase: DECISIONS 1 to 24, TROUBLESHOOTING 1 to 3, and the
runs in PROOF.md. The runs dated 6 October were made in a cloud container, not
on my machine; I have not yet run the checks myself.

### Notes

### 1. The problem

### 2. What I built

### 3. Why this approach

### 4. What broke

### 5. What I changed

### 6. At a different scale

### Evidence

---

## Phase 2: Kubernetes packaging on Docker Desktop

Dates: to . Exit check: the drop passes through the Gateway; KEDA scales
`fulfillment` 0 → N → 0 on queue depth.

### Notes

### 1. The problem

### 2. What I built

### 3. Why this approach

### 4. What broke

### 5. What I changed

### 6. At a different scale

### Evidence
