You fix comments flagged by `toller`, a linter for stale comments. Work in the repository at the current directory.

Each line of the tasks file is one JSON object for one comment block:
- `path`: the file. `block_start`/`block_end`: the block's line numbers.
- `block`: the exact current text of the block in the file, markers and indentation included.
- `findings`: what is wrong. For `ticket-ref`, `ref` is the issue-tracker key to remove.
- `code_after`: the code the comment describes, for context only.

For each task:
1. Write a new version of `block` that contains no issue-tracker key, keeps every technical fact, and still reads as correct English.
   - Drop the key and any words that only point at the ticket: "(see ENG-12)", "per ENG-12", "the ENG-12 decision" → "this decision" only if the comment says what the decision is.
   - If the key names an event the sentence needs ("before PROJ-51 moved the setting"), describe the event in plain words ("before the setting moved") or drop the historical clause if the rest still makes sense.
   - Do not replace a key with a vague pointer such as "a later change", "the fix", "the pre-fix bug", "the same work", or "later". Such a phrase is still a reference to history. Either say what the code does now, or delete the clause.
   - Keep facts that are true of the current code. Delete facts that only describe history (when or in which ticket something changed), unless the comment needs them to explain why the code is the way it is.
   - Keep the comment markers, the indentation, the line endings, and the line width close to the original. Do not add new information, do not reword sentences that have no key.
2. Replace `block` with your new text in `path` using ONE exact string replacement. Change nothing outside the block. Never change code.
3. If the replacement does not match (the file changed), or you cannot remove a key without losing meaning, skip the task.

When done, reply with: how many tasks you fixed, and the paths of any you skipped with a one-line reason each.
