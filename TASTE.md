# TASTE — design scars

Prior design rulings for bwg-cli. One entry per real rejection, each
with its why — a rule without a why fossilizes into style police.
Read this before any design verdict; delete a scar when its expiry
condition arrives.

The test behind all of them: a surface that gets prettier while the
task gets harder is a costume, and it fails no matter how clean it
looks in a screenshot.

---

## 2026-08-07 rejected: read-only as a CLI flag

**Why.** The obvious shape for `--read-only` is a check in each
mutating command: resolve the server, look at the flag, refuse. It was
rejected before it was written. That design makes the guarantee equal
to the number of places somebody remembered to check it, which is a
guarantee that decays with every new command — and it offers nothing at
all to the MCP server or to anyone importing the Go package, who are
exactly the callers that most need it. A safety property you have to
re-implement per surface is a convention, not a property.

**Reuse.** Put a safety gate at the narrowest point every path must
cross, and derive the surfaces from it. Here that is `Client.call`,
in front of the HTTP client, reading one registry (`kiwivm/ops.go`).
The CLI, the MCP tool list and `bwg api ops` all read the same table,
so they cannot disagree. Then test it structurally: the reflection test
discovers every client method and its endpoint rather than listing
them, so a method added next year is covered without anyone
remembering.

**Expires.** If the SDK is ever split from the CLI into separate
repositories, the registry has to stay with the client, not the CLI.

---

## 2026-08-07 rejected: the destructive tier that swallowed everything

**Why.** The first classification pass marked `stop`, `restart` and
`resetRootPassword` destructive alongside `reinstallOS`, on the reflex
that anything user-visible deserves the loudest warning. That put over
half the endpoints in the top tier. A confirmation that fires on
`bwg restart` teaches people to hammer `y` — and then it fires on
`bwg os reinstall` and gets the same reflex. Marking everything
dangerous is indistinguishable from marking nothing dangerous, while
looking far more responsible in a code review.

**Reuse.** Tier by an answerable question, not by vibe: *can another
call in this package undo it?* Stop is a write because Start undoes it.
Snapshot delete is destructive because nothing brings it back. Then
enforce the discipline mechanically — every destructive entry must
state what is lost (`Op.Why`), and a test fails if one does not, and
another test fails if the destructive set becomes a majority. A tier
you can't justify in one sentence is a tier you haven't earned.

**Expires.** Not expected to. Revisit only if KiwiVM adds an undo for
something currently in the destructive set.

---

## 2026-08-07 rejected: the consent prompt that withheld the deciding fact

**Why.** The first `snapshot restore` prompt read "Restore snapshot
1347645_20260801_aaaa.tar.gz? [y/N]". Everything needed to answer it
was already in memory — the snapshot's age, its OS, its description,
the hostname and IP of the box about to be overwritten — and none of it
was on the card. The user was being asked to decide with less
information than the program had. Worse, the name means nothing: those
strings are unguessable and two of them differ by four characters, so
the prompt could not catch the one mistake that actually happens, which
is restoring onto the wrong server.

**Reuse.** A consent prompt is a decision surface, not a speed bump.
Whatever the program already knows that the decision depends on belongs
ON the card (`Consent.Facts`), and a destructive card must say what is
lost. Where the real risk is *wrong target* rather than *wrong action*,
y/N cannot help: make them type the server's name. Keep that tier to
the four operations that replace the whole box, or people paste names
reflexively and it stops protecting anything.

**Expires.** If a fifth operation genuinely replaces the whole box, it
joins the set. Anything less does not.

---

## 2026-08-07 rejected: --yes as a silent bypass

**Why.** `--yes` started as a pure skip: no prompt, no output, straight
to the API. That is what "skip confirmation" sounds like it should
mean. But the runs that pass `--yes` are exactly the unattended ones —
cron, CI, an agent — and those are the runs where nobody watched it
happen. Skipping the prompt is reasonable; skipping the *record* leaves
a mutation with no witness.

**Reuse.** Separate the question from the account of what was done.
`--yes` suppresses the question and still writes one line to stderr
naming the operation, the target and the server, marked `(--yes)`.
Honest feedback is how the user stays informed when a confirmation is
not there to do it. Stderr, not stdout — a record must never
contaminate piped data.

**Expires.** No.

---

## 2026-08-07 rejected: refusing writes by prompting into a closed pipe

**Why.** The first non-interactive path called the prompt anyway,
reading from a stdin that was `/dev/null`, getting EOF, and treating
that as "no". It worked, in the sense that nothing was mutated. But the
error read "declined", which is a lie: nobody declined anything. A cron
job would report that a human said no. Worse, with stdin attached to a
pipe that never closes, it would have hung forever.

**Reuse.** Detect that no human can answer (`output.Interactive`) and
refuse *before* asking, with an error that names `--yes` as the fix and
exit code 3 to distinguish "understood and deliberately not done" from
"failed". Never ask a question into a channel that cannot answer it.

**Expires.** No.

---

## 2026-08-07 rejected: preflight validation ahead of the read-only refusal

**Why.** `bwg net ipv6 add` checks the plan's subnet limit before
prompting, so it can say "you are at your limit" instead of surfacing a
raw API error. Correct — except under `--read-only`, where it reported
"this plan allows 1 IPv6 /64 subnets and 1 are assigned" and exited 1.
Every word true, and the wrong answer: nothing was going to happen for
a completely different reason, and the exit code said "your request was
invalid" instead of "I refused". A test caught it; a user would have
spent ten minutes freeing a subnet first.

**Reuse.** When several reasons could stop an action, report the one
that is categorically prior. `App.ClientForOp` refuses a forbidden
operation at client-resolution time, before any preflight round trip.
The same rule drove `Confirm` checking read-only before drawing the
card: being walked through a decision and then told it was never
possible is worse than a plain refusal.

**Expires.** No.

---

## 2026-08-07 rejected: the "error" field on every public SDK type

**Why.** The obvious port of the API's shape embeds the response
envelope in every struct, the way the prior-art client does — so
`ServiceInfo` carries `Error int` and `Message string`. It costs
nothing to write and it poisons everything downstream: every `--json`
payload gains a meaningless `"error": 0`, an agent has to be told to
ignore it, and a Go caller sees a field called `Error` that is not an
error and does not implement the interface.

**Reuse.** Decode the envelope separately from the payload — two
unmarshals of the same bytes, which is cheap — and let the public types
be pure data. Errors are `error` values; the wire format's framing
stops at the package boundary. The one endpoint that genuinely overloads
the envelope (`basicShell/exec`, where `error` is the command's exit
status) gets an explicit type saying so, rather than everything else
paying for its exception.

**Expires.** No.

---

## 2026-08-07 rejected: advertising MCP tools that would be refused

**Why.** The first MCP server exposed all fourteen tools regardless of
mode and returned an error from the mutating ones under `--read-only`.
Symmetrical with the CLI, and wrong for the medium: an agent reads the
tool list as the statement of what is possible, tries the tool the task
seems to need, gets refused, and — being an agent — tries again with
different arguments. The honest surface costs nothing.

**Reuse.** When the consumer is a model choosing from a menu, remove
the dish rather than declining the order. The list is the contract.
Keep the call-time check as well, since a client can call anything it
likes and the guarantee must not depend on it having read the list.

**Expires.** If MCP grows a real consent channel, the write tools can
be advertised with an approval step instead of hidden.

---

## 2026-08-08 rejected: trusting Go's error text with a credential

**Why.** Reads go out as GET, so the `api_key` is in the query string.
Go's `net/http` wraps a dial failure in `*url.Error`, which renders the
*whole URL*. Running `bwg ls` against a fleet whose endpoint was down
printed three working API keys to the terminal — from a plain
"connection refused". Every layer above was careful: `--json` masks
keys, `--verbose` logs no parameters, config is 0600. None of that
matters when the standard library helpfully includes the URL in an
error nobody thought of as output.

It was found by running the tool for a README screenshot, not by any
test. That is the lesson underneath the lesson: the audit surface is
everything that can reach a terminal, and error paths are the part
nobody looks at.

**Reuse.** Sanitize at the boundary where the secret is known — the
client, which holds the key — not at each print site. Two constraints
the first fix got wrong and the tests now pin: keep the error *chain*
intact (`errors.Is(err, context.DeadlineExceeded)` must still work, so
wrap rather than rebuild with `errors.New`), and refuse to substitute
a key shorter than 8 characters, or a test placeholder of `"k"` turns
`api_key` into `api_REDACTEDey` and corrupts the diagnostic while
protecting nothing.

**Expires.** No. If a new transport is added, it needs the same
treatment.

---

## 2026-08-08 rejected: an "affected" badge on the status correlation

**Why.** Matching status-page incidents against the fleet first
rendered as a red `AFFECTED` tag next to a server name. It looked
authoritative and it was a lie about the evidence: the input is prose
written for humans ("VMs hosted on nodes v31xx, v32xx"), and the match
is a regex for node prefixes plus a list of place names. A badge
asserts a fact. What bwg actually has is an inference from two
recognisers that will both miss incidents phrased in ways nobody
anticipated.

The failure mode that decided it: someone sees no badge, concludes the
provider is fine, and spends an hour debugging their own application
during an upstream outage the matcher did not recognise. The badge
converts "I did not find anything" into "there is nothing", which is
the one thing a heuristic must never be allowed to say.

**Reuse.** When output is an inference, ship the reasoning with it —
"names node group v31xx, and this server is on v3105" — so the reader
can evaluate the inference instead of the tool's confidence. And state
the negative case explicitly: every render ends with "a match is a
prompt to look, and no match is not an all-clear". A server that could
not be reached is reported as *unchecked*, never folded into
unaffected, for the same reason.

**Expires.** If BandwagonHost ever publishes structured incident data
naming affected VEIDs, the inference becomes a fact and the hedging
should go with it.

---

## 2026-08-13 rejected: two spellings for one credential

**Why.** v0.1.0 accepted the API key as either `BWG_API_KEY` or
`BWG_KIWIVM_API_KEY`, on the reasoning that the second matches the
"KiwiVM API key" label in the panel and costs one extra `os.Getenv`.
What it actually cost: the code called the short one canonical, and
every README, skill file, install script and error message taught the
long one. So the pair a new user copied read

    export BWG_VEID=1347645
    export BWG_KIWIVM_API_KEY=private_xxx

— two halves of one credential, spelled in two different styles, one
of them namespaced twice. Nothing in the tool could tell you which was
real, because both were. An alias is not free: it is a second name
that every surface must independently choose between, and they will
not agree.

**Reuse.** One name per thing, and let the prefix do the namespacing —
`gh` uses `GH_TOKEN`, not `GH_GITHUB_TOKEN`. `BWG_VEID` and
`BWG_API_KEY` read as the pair they are. Vendor labels belong in the
prose that explains where to find the value, not in the variable name.

Keeping the old spelling working is separate from documenting it: it
is accepted, mentioned nowhere except one line of the README, and
warned about once per run on stderr naming the replacement. A
compatibility path that stays silent is a compatibility path forever.
`bwg server show env` reports whichever variable is actually in play,
because a tool that will not tell you where its credentials came from
is the reason this was confusing in the first place.

**Expires.** `BWG_KIWIVM_API_KEY` goes away at v1.0. The warning goes
with it.

---

## 2026-08-13 rejected: a total that outlived its window

**Why.** `bwg usage` defaulted to `--days 0`: every sample KiwiVM
kept, which after two years is 600-odd rows, oldest first. The command
answers "when did the traffic spike", and it put the recent end of the
series off the bottom of the screen along with the quota summary. It
was defensible as "we do not hide data" and it was unusable.

The real defect was underneath: `--days 7` trimmed the table but not
the arithmetic, so seven rows sat above `Total: 5.9 TiB ... over 608d`
and `--raw` ignored the window entirely. Three parts of one screen
describing three different spans, each of them individually correct.

**Reuse.** Trim the *data*, once, and let every renderer read the same
slice — the table, `--raw`, the totals line and the JSON payload
cannot then disagree. Pick a default that matches the question: the
window is 30 days because the quota printed underneath it is monthly.
And say what was withheld — "Showing 30 of the 608 days KiwiVM kept —
for all of it: bwg usage --days 0" — because a table that silently
stops reads like a box with no history, which is a worse lie than the
one truncation was meant to avoid.

**Expires.** No. If a command ever needs two windows on one screen,
each has to label its own.
