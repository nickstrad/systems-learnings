# Walkthroughs

Guided tours of each project, kept current with the code (see
`AGENTS.md`). Each project has two:

- **Code:** the non-test code in suggested reading order, from the
  domain outward to the binaries, tied to the paper.
- **Tests:** every test file, grouped by the categories in
  [`../testing.md`](../testing.md), with what each kind of test buys us
  and a one-line command to run each one.

| Project | Code | Tests |
|---------|------|-------|
| Jump Consistent Hash (`jumphash/`) | [code.md](jumphash/code.md) | [tests.md](jumphash/tests.md) |
| Log-Structured Merge-Tree (`lsmtree/`) | [code.md](lsmtree/code.md) | [tests.md](lsmtree/tests.md) |

## Diagrams

Architecture diagrams follow the C4 model and appear in two forms side by
side: a Mermaid C4 block that GitHub renders, and a Unicode text version
for terminals, drawn with the `draw-visual` skill from the sources in
each project's `diagrams/` folder. To redraw the text versions after
editing a source:

```sh
python3 ~/.claude/skills/draw-visual/scripts/render.py --update docs/walkthroughs/<project>/code.md
```

Change the `.mmd` source and the Mermaid block next to it together.
