# Guide pages

Two pages explain the project and are published as Claude artifacts.

| Page | Source | Published at |
|---|---|---|
| TicketDrop Field Guide | `src/field-guide.html` | https://claude.ai/artifact/SXE3mjmuBPCnzeEr3witSY |
| TicketDrop Phase 2 Workbook | `src/phase-2-workbook.html` | https://claude.ai/artifact/RD6kXt4bFr9hpiunekJ73J |

Edit the files in `src/`, never the built copies beside this file.

```bash
python3 docs/diagrams/build.py   # only when a diagram changed
python3 docs/guide/build.py      # inlines the stylesheet and diagrams
```

Then republish each built page to its existing URL so the link stays the same.

The diagrams are defined in `docs/diagrams/build.py`. It writes each one as an
`.excalidraw` file, to open and edit in Excalidraw, and as the `.svg` the pages
embed. A change made by hand in Excalidraw is lost on the next build, so change
the script.
