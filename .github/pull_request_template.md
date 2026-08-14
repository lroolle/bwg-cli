## What this changes

<!-- One paragraph. Why it matters, not just what moved. -->

## What you ran

<!-- Paste it. "Tests pass" is not evidence; the output is. -->

```
make check
```

## Checklist

- [ ] `make check` passes (gofmt, vet, race tests, build)
- [ ] New behaviour has a test that fails without the change
- [ ] Touched a command's flags or JSON shape → `skills/bwg-cli/SKILL.md` and `llms.txt` updated with it
- [ ] Added or reclassified a KiwiVM endpoint → `kiwivm/ops.go` states its risk, and a destructive entry says what it loses
- [ ] Anything user-visible → a `CHANGELOG.md` entry under Unreleased
