---
name: test-first
description: Write the failing test before the code. Use when adding behaviour or fixing a bug.
---

1. Write the smallest test that fails for the right reason. Run it and read the failure; if it fails for another reason, fix the test.
2. Write the least code that makes it pass. Run the test again.
3. Refactor only while the tests are green.
4. For a bug, the first test reproduces the bug exactly as reported.
5. Name tests after the behaviour ("rejects dates before 1970"), not the function.
