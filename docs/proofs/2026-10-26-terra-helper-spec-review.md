# Restricted helper design review

Date: 2026-10-26. This retains the final independent Astra verdict on the shared
[helper contract](../plans/2026-10-26-terra-helper-contract.md). It qualifies the
design and task coverage, not an implementation, live API or factory delivery.

```json
{
  "verdict": "PASS",
  "review_scope": "design_and_task_coverage_with_targeted_codec_amendment",
  "spec": "docs/plans/2026-10-26-terra-helper-contract.md",
  "verified_sha256": "1ae5a99aff3dcf2bf9863f9365327ec5d85810e7a834249c80ebbb4c582da53d",
  "reviewer": "/root/astra_helper_spec_review",
  "model": "gpt-6-astra",
  "reasoning_effort": "max",
  "remaining_findings": [],
  "implementation_qualified": false,
  "live_effects_qualified": false,
  "prior_structural_review_sha256": "390cc7622c11c866187fdde530f9712907e783e015ab23677eea070e35f9dff6",
  "fresh_context_initial_review": true
}
```

The fresh reviewer read actual producer, controller, host and candidate code and
the eight implementation-task criteria. Initial revision `243f7dbb…` failed:
two existing runtime comparisons would compare optional config pointers by
allocation identity, rejecting unchanged rereads in readiness and cleanup.
Recording-process injection and payload composition also needed clarification.
Revision `cebde7e6…` closed those findings but its exact prompt schema omitted
required architecture source/base/check and protected review context.

Final revision `390cc762…` closes the remaining finding. Shared typed context
now carries trusted source, seed, policy and check identities, bounded complete
source files, and candidate-bound protected acceptance projection with the
original output hash. Role tests must inspect the actual count and Responses
payloads, including an initial objective containing no identities. Complete
context participates in request hashing and size validation before accepting a
plan or feedback and before reserving another invocation.

The reviewer reported no remaining structural gaps in producer/consumer schema,
workspace/receipt semantics, composition failure routing, the private recording
build seam, or task ownership/dependencies. A subsequent targeted review accepted amendment `1ae5a99a…`: result codecs stay
private until a genuine cross-package caller exists, and bounded temporary
parsing may precede exact canonical rejection before trusted use. No wire field,
limit or authority rule changed; the prior structural verdict remains applicable.
The final verdict is conditional on the full current hash above; later changes
require another review.

Root independently matched the full SHA-256 above and ran:

```sh
/Users/as21/codehouse/oro/node_modules/.bin/markdownlint-cli2 \
  --config .markdownlint.yml docs/plans/2026-10-26-terra-helper-contract.md
```

Result: one file, zero errors, exit zero. Gate `oro-mf-terra-spec-proof` retains
this artifact path and hash in its closure; only that matching PASS permits
`oro-mf-terra-entry` to start. Live pricing/count fees, credentials, offline
runtime supply, native effects, recovery and sustained exact-delivery trials
remain explicit parent gates. No model, credential or native runtime action was
performed by this design review.
