# BPS compatibility and capacity audit — 2026-09-27

## Scope and deployment state

Baseline: 4361085222e490b4989ef5d1ec7e17f59e76d070 (v0.0.11 source). Candidate branch: fix/bps-scale-audit-20260927. This audit changes the local candidate only. Production remains v0.0.11; this document is not a deployment or unlimited-capacity certification.

The inventory hashes 4,368 tracked Go/TypeScript/Vue/JavaScript/SQL/YAML/shell files, totaling 1,459,276 lines, including generated code and tests. Whole-tree automated checks and targeted manual review are different evidence. Manual review concentrated on BPS routing, tools, Office schema references, images, replay caching, stream conversion, ingress memory and their direct callers. It would be inaccurate to claim every application line was manually verified or that arbitrary upstream failures have been eliminated.

## Reproduced defects and changes

| Area | Observed defect | Candidate change |
| --- | --- | --- |
| Repeated image history | Full entry/byte quota rejected an already retained identical image | Bounded fingerprint lookup before new file/quota reservation; scope separation, MIME validation and temporary retention pins |
| Image fallback | Download pool overload could turn an unknown token into 503 | Resolve ownership/expiry before taking a download slot; unknown tokens remain 404 |
| Slow downloads | Real slow TCP consumer retained its slot and an expired file after request deadline | Per-image network write deadline, cancellation interrupt, final flush within deadline and synchronized deadline cleanup |
| Public download handler | Invalid probes queried settings; a known image's settings outage became 404 | Validate and resolve existing ownership first; unknown tokens skip DB; known-image transient settings outage gets 503 + Retry-After; no settings bypass |
| Large text histories | Long non-image JSON requests released their processing reservation before upstream completion | Requests at least 1 MiB retain the existing 8x processing budget; small text keeps independent headroom |
| Tool identity | Explicit namespaces could resolve to a same-named global function | Exact catalog identity, bounded tool-tree nesting, duplicate tool IDs rejected before dispatch |
| Office parameter schema | Local JSON Pointer array references/root identifiers could be rejected | Support verified local references; external references/cycles/resource-boundary ambiguity remain explicit unsupported cases |
| Replay cache | Decode and fingerprint work held a global cache mutex | Immutable cached bytes; hash/decode outside the lock, independent returned objects |
| Native features | Native history/options could be silently lost through BPS | Route verified native-only constructs to native forwarding; do not promise unsupported sampling/output controls |
| Chat streams | Global text-seen state lost message suffixes/additional messages; missing item IDs could duplicate text | Per-item/content prefix digest and unambiguous ID aliases; recover only exact missing suffixes |
| Fragmented tool arguments | Repeated growing string copies caused quadratic allocation | Prefix byte count and SHA-256 state; append only verified terminal suffix |
| Buffered Responses/Chat | Empty terminal output discarded text already received in deltas | Bounded reconstruction preserving item/type/part/order, at most 16 MiB text, 1,024 items and 4,096 parts |
| Retry memory | Every forwarding attempt cloned the full image-heavy body | Retain immutable original input for the existing bounded retry; exact input preservation tested |
| Capability diagnostics | Some unsupported controls were silently ignored | X-BPS-Ignored-Parameters reports parameter names only; it is observability, not enforcement |

Cross-review corrected an intermediate handler design before acceptance: returning 503 for every settings failure would block 404-based old-instance image fallback. The final handler first proves local ownership. An initial broad vet ran while files were changing and saw inconsistent imports/types; its failure is retained, and final checks use stable sources.

## Compatibility boundaries

The first whole-backend unit run exposed a timing assumption in the existing OAuth image JSON keepalive test: sleeping 20 ms did not guarantee that a heartbeat had run under load. The fixture now waits for the synchronized heartbeat event before releasing the upstream response, while retaining the flush, valid-JSON and image-content assertions. This changes test synchronization, not production heartbeat behavior.

BPS is an adapter to an upstream product endpoint, not a documented replacement for every native Responses/Chat/Codex capability. Exact tool identity and executable tool-argument JSON must remain valid. Malformed generated tool JSON must not be guessed into executable calls. Existing recovery is bounded and only before client output/tool dispatch; retries after partial execution could duplicate user actions.

Original image detail, supported inline/HTTPS images, client custom/function/function-code transport and known native history cases retain the existing protocol matrix coverage. Public OpenAI image size/count limits are not evidence for the private BPS endpoint's capacity and are not copied into this gateway blindly.

Remaining gaps include generic Chat input_audio/refusal preservation, true parallel tool semantics, some native effort/control differences, and controls that the native forwarding path also strips. Unsupported max_output_tokens/sampling/truncation/store behavior is not made equivalent merely by switching route. A names-only diagnostic header is added where BPS ignores meaningful controls. Header visibility to browser JavaScript still depends on the existing CORS policy.

## Measured local evidence

- 64 concurrent warm-cache reuses at entry/byte capacity: identical capability, one retained file, no residual reservations.
- Repeated 1 MiB image microbenchmark: median 29.56 to 18.30 ms/op, about 38% lower elapsed time. Bytes allocated remain about 5.64 MB/op; JSON remains material.
- Parallel replay microbenchmark: median 1.185 to 0.652 ms/op, about 45% lower elapsed time, without a meaningful allocated-byte reduction.
- Fragmented 256 KiB tool argument microbenchmark: 24.54 to 2.31 ms/op; allocated bytes about 34.58 MB to 0.34 MB. This covers fragmented native/shared Chat conversion, not total BPS or image throughput.
- 24 concurrent cancelled forwarding calls exercise body/goroutine cleanup.
- Real TCP slow-reader and blocked-write cancellation fixtures fail on the baseline and pass on the candidate.

These are bounded local regression/microbenchmark observations. They do not establish production maximum QPS, thousands of concurrent generations, or performance under a long real-provider load. Complete commands, stdout, stderr and exit statuses belong to the external verification ledger; final checks and their results are listed there.

## Capacity and deployment recommendations

Retained image cache defaults remain 512 entries, 1 GiB, 32 simultaneous downloads, 30-minute retention; image download timeout is now 120 seconds, configurable from 1 to 600 seconds. The entry default corresponds to roughly 17 new retained images per minute for a full 30-minute window, before accounting for burstiness or the byte limit. Repeated images within the same scope reuse capacity; new images do not. A 5 MiB average hits the byte quota before 512 entries.

New startup settings are under gateway.image_relay_cache and matching GATEWAY_IMAGE_RELAY_CACHE_* environment variables: MAX_ENTRIES (1..100000), MAX_BYTES (1 MiB..64 GiB and representable by the platform), MAX_DOWNLOADS (1..256), DOWNLOAD_TIMEOUT_SECONDS (1..600). Examples and all four Compose variants pass these variables through. Zero-valued programmatic settings retain defaults; invalid explicit ranges fail validation.

Production inspection observed 8 CPUs, about 15.1 GiB RAM, substantial available RAM/disk and no restart/OOM on the active container. It also observed no Docker memory/CPU/pid limits, no GOMEMLIMIT, multiple old application containers, a 1 GiB processing reservation budget and max_requests=128. These are observed configuration values, not a tested capacity guarantee.

The 8x processing reservation makes 1 GiB correspond to at most 2 simultaneous 64 MiB bodies or 16 simultaneous 8 MiB bodies, regardless of a 128 request ceiling. Decode concurrency=4 with the default 512 MiB decode budget can effectively allow just one worst-case compressed body. Keep decode admission, processing admission, download slots, retained disk cache, scheduler/account limits and upstream TPM/RPM separate.

Recommended next rollout is a small canary, then measured increments. A possible starting profile for the inspected host is 8,192 retained entries / 8 GiB disk / 64 downloads, a 1 GiB decode reservation budget, and unchanged 1 GiB processing reservation; consider an 8 GiB container cap with GOMEMLIMIT around 6 GiB, leaving host/DB/Redis headroom. These proposed numbers are not applied and must pass representative mixed-image/text, slow-reader and cancellation soak tests before broad rollout. Never increase all admission limits together without measuring RSS, queue wait, disk I/O and upstream tokens.

Do not load-balance ephemeral image capabilities across arbitrary replicas. Token ownership and metadata are process-local: retain instance affinity and drain the old owner until tokens expire, or design shared TTL storage/metadata plus ownership routing first. Old containers cannot simply be stopped while they still own live images; confirm fallback retention and worker/database activity before retiring them.

Outstanding scale work: avoid duplicate temporary reservations for simultaneous cold uploads of the same image; assess O(n) full-cache cleanup at unusually large configured entry counts; add tenant fairness, account/model capability-aware routing, and observable admission/queue metrics. Network deadlines rely on ResponseWriter wrappers preserving Unwrap; a custom blocking writer without deadline support cannot be preempted safely by context checks alone.

## Production error interpretation

Read-only inspection after the v0.0.11 cutover still found model-access 403, upstream token-rate 429, transient 5xx, a malformed generated tool JSON failure and incomplete upstream responses. Some rows with upstream errors ended with client status 200 after recovery and must not be counted as final failed requests. No error rate is claimed from a mixed or incomplete denominator. Account/model authorization and upstream quota require capacity/routing or provider-side remedies; permissive JSON parsing or unlimited retries cannot safely fix them.

## Acceptance and rollback

The original source stays byte-preserved. The delivery uses the same four roles: MODIFIED_FILE.tar, DIFF_FILE.patch, VERIFICATION.txt and executable ROLLBACK.sh, with the baseline archive alongside. Matching baseline/candidate/restored regression commands, fixture hashes, literal outputs, archive hashes and a tested rollback are required before final delivery. Production remains unchanged by this audit.
