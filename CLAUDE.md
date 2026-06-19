## Working protocol

- **Exploratory questions** ("how could we…", "what do you think about…"): propose an approach and wait for explicit confirmation before implementing. Do not start work on the assumption that exploration implies approval.
- **Non-trivial plans** must be reviewed before presenting to the user:
    1. Draft the plan, ask exploratory questions do not assume.
    2. Once drafted, spawn three `Agent` calls in parallel: one security-and-consistency reviewer, one adversarial reviewer, one UX reviewer.
    3. Incorporate suggestions that hold up.
    4. Present the final plan to the user with a short "reviewer input adopted" section so the user can see what shifted.
- **Suspected bugs in existing code**: do not write tests that lock in behavior you suspect is wrong. Stop, describe the concern, ask the user how to proceed.

## Code style

- Import grouping: stdlib, blank line, external packages, blank line, internal packages.
- We use american english - `customize` not `customise` 
- Error wrapping: `fmt.Errorf("%w: %w", ErrOuter, err)`.
- No emojis in code, tests, or documentation unless the user explicitly asks.
- Expand compound `if` statements: prefer

  ```go
  x, err := thing()
  if err != nil {
      return err
  }
  if x == 1 {
      ...
  }
  ```

  over `if x, err := thing(); err == nil && x == 1 { ... }`.

## Do not, without asking first

- Add new top-level packages.
- Add, remove, or upgrade external dependencies (including Go toolchain version).
- Change public APIs outside the scope of the requested task.
- Modify `ABTaskFile`,any `Dockerfile`, or CI configuration.
- Run destructive git operations (force-push, reset --hard, branch -D) or skip hooks.