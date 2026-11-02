# Repository instructions

This repository contains Go systems projects exploring the ten papers in
`docs/papers/`. Each paper's project lives in its own Go module.

## Keep project documentation current

When a paper project becomes runnable or is completed, update the root
`README.md` in the same change:

- Add or update its entry under **Run the projects**. Use the `jumphash`
  section as the example: include prerequisites, the working directory,
  build and run commands, useful interactions, expected results, and how
  to stop or reset the project.
- Check the commands against the implementation and verify the walkthrough.
- Update the paper table's project link and status, plus any progress or
  remaining-work notes. Mark a paper complete only when its stated work
  is finished.
- Link to the project's own README for detailed documentation, and keep
  that README consistent with the root instructions.

When a project's commands, flags, setup, or behavior change, update its
existing usage section even if the paper project is still in progress.

`AGENTS.md` is the source of these instructions. `CLAUDE.md` is a relative
symlink to it; preserve the symlink and edit this file to change the rules.
