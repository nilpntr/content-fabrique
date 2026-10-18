# Third-party notices: motion graphics (D-35)

No third-party code is copied, ported or vendored in this package, in its template bundle (`bundle/`) or in cf-motion
(`packages/media-tools/motion-runner`). Two projects informed the design, and are credited here:

- **open-edit** (https://github.com/veedstudio/open-edit), Copyright 2026 VEED LIMITED, licensed under the Apache License,
  Version 2.0. Its page runtime showed the approach we took: replace the page's clock with a virtual one, pause every animation and
  set its current time for each frame, capture with transparency. Our runtime (`bundle/runtime.js`) and runner are our own code.
- **herk-edit-kit** (https://github.com/nateherkai/herk-edit-kit), MIT licence. Its `MOTION_PHILOSOPHY.md` gave the motion
  vocabulary the templates follow (word-by-word reveals with scale and blur, type as a character, a new element every 0.3 to
  0.6 s, holds long enough to read). No code or assets were used.

The software that runs the templates (chrome-headless-shell from Chrome for Testing and the libraries it loads) ships in the
media-tools image with its licence texts; its inventory and the licence policy are in `packages/media-tools/LICENSES.md`.
