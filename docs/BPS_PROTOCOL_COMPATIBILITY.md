# BPS / Codex / Office compatibility audit — 2026-09-27

This audit distinguishes public API contracts, official Codex client payloads, Office host APIs and this gateway's BPS adapter. A ChatGPT product endpoint is not documented as implementing every public API feature.

## Official sources

Reviewed documentation and source on 2026-09-27:

- [Images and vision](https://developers.openai.com/api/docs/guides/images-vision): URLs, inline data, formats, model-dependent detail and image limits.
- [Responses migration](https://developers.openai.com/api/docs/guides/migrate-to-responses): Responses items versus Chat messages and state handling.
- [Function calling](https://developers.openai.com/api/docs/guides/function-calling), [tool search](https://developers.openai.com/api/docs/guides/tools-tool-search), [streaming](https://developers.openai.com/api/docs/guides/streaming-responses): tool ownership, dynamic catalogs and terminal events.
- [GPT-6 guidance](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-6), [GPT-5 guidance](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5), [reasoning](https://developers.openai.com/api/docs/guides/reasoning): effort, modes and supported APIs vary by model. Public capability does not prove BPS account access.
- [ChatGPT for Excel and Google Sheets](https://help.openai.com/en/articles/20001063-chatgpt-for-excel-and-google-sheets), [PowerPoint](https://help.openai.com/en/articles/20001242-chatgpt-for-powerpoint): official host integrations. Codex in the desktop app can use the Excel add-in; this gateway itself has no open workbook.
- [Microsoft Office JavaScript API](https://learn.microsoft.com/en-us/office/dev/add-ins/develop/understand-the-javascript-api-for-office), [requirement sets](https://learn.microsoft.com/en-us/office/dev/add-ins/develop/office-versions-and-requirement-sets): host-specific execution and feature availability. Changing gateway JSON cannot make unsupported Excel APIs execute.
- Official Codex source pinned to [67a709665ac7b50311b93e32612c9a8281684787](https://github.com/openai/codex/tree/67a709665ac7b50311b93e32612c9a8281684787). Inspected protocol/src/models.rs, tools/src/responses_api.rs, core/src/tools/handlers/view_image.rs and view_image_spec.rs under codex-rs. The image handler emits application/octet-stream data URLs; image detail includes original. Tool identities and raw source strings must survive round trips.
- Official generated SDK types: [Chat forced function](https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_named_tool_choice_param.py), [Responses forced function](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/tool_choice_function_param.py), [Responses input image](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_input_image_param.py). These confirm nested versus flat function names and image URL/file-ID/detail fields.

The Excel help article links to https://bps.openai.com/basispoints/api/office/manifest.xml. The web reader could not render XML and a direct fetch returned HTTP 403. No public BPS OpenAPI contract was located. BPS behavior is verified using project code, production diagnostics and controlled probes, not inferred solely from public API docs. The Responses reference exceeded the web reader size cap; focused guides and generated SDK types supply the relevant contracts.

## Endpoint and ownership map

| Surface | Endpoint / transport | Responsibility |
|---|---|---|
| Gateway Responses | /v1/responses and existing aliases | Responses history, tools, lifecycle and billing |
| Gateway Chat | /v1/chat/completions | Convert messages/choices; return Chat JSON or SSE |
| Excel BPS | https://bps.openai.com/basispoints/api/responses | Account-scoped product inference through the project's adapter |
| Native Codex | https://chatgpt.com/backend-api/codex/responses | Capabilities BPS cannot represent; account/model access still applies |
| Public API | https://api.openai.com/v1/responses and /v1/chat/completions | API-key contracts; not interchangeable with product authentication |
| Office.js | Client run_officejs / declared tools | Excel/PowerPoint host executes document operations |
| External tools | Function JSON, FUNCTION_CODE, CUSTOM | Gateway translates; client executes; no server-side evaluation |

## Corrections in 0.0.11

1. **Codex binary images:** accept application/octet-stream only when image metadata identifies PNG/JPEG/GIF/WebP. Serve detected MIME and retain bytes. MIME mismatch, malformed base64 and excessive dimensions still fail explicitly.
2. **Image history capacity:** identical inline URLs share a decode/file/token. Unique decoded bytes are counted once even for different declarations of identical bytes. Preserve every occurrence, position and detail. Limits: 500 inline occurrences, 50,000,000 unique decoded bytes/request, 20 MiB/image, 64 megapixels. Disk quotas, expiry, admission budgets and tenant-scoped signatures remain. These are gateway limits, not a guarantee that every upstream accepts every boundary.
3. **Detail preservation:** Chat → Responses, Responses → Chat and tool media retain auto/low/high/original and signed URLs. Missing detail stays missing. Retain the 0.0.10 original-detail whitelist fix; never globally downgrade original to high.
4. **Instruction roles:** Chat developer remains developer for both string and multipart messages.
5. **Forced function shape:** nested Chat function.name becomes flat Responses name. Conflicting names or extra nested data are not silently discarded. Native routing preserves forced-tool semantics.
6. **Office/source schemas:** bounded local references through $defs/definitions, nullable unions and escaped JSON Pointer names identify explicitly declared string sources. Remote references, cycles, numeric sources and ambiguities do not activate raw transport. Generic JSON transport remains available.
7. **Capability routing:** hosted tools, async/native shell declarations, native history, file-ID images, audio/file media, previous_response_id and non-standard reasoning modes use native Codex instead of being dropped or pretending BPS executed them. Headers expose the bypass reason. No model substitution.
8. **Chat uses BPS when enabled:** share BPS auth, relay, tool validation, pre-output recovery, timeouts and usage. Convert only downstream format. Completed/incomplete/failed events map to Chat chunks, finish reasons or errors, with one DONE marker. Terminal-only text is emitted without repeating streamed text; usage chunks follow stream_options.include_usage.

## Acceptance matrix

| Case | Handling / verification |
|---|---|
| Four image formats, explicit MIME and octet-stream | Byte-identical relay matrix |
| HTTPS/signed URL, missing/auto/low/high/original | Conversion matrix and existing original-detail suites |
| Repeated large images / 25-image history | Shared payload, all occurrences preserved |
| MIME/base64/quota/dimension/count errors | Explicit rejection and reservation/file cleanup |
| Office source fields and local Schema references | 15 field/reference combinations; exact source/metadata replay |
| Unicode/CRLF/quotes/backslashes/null/false/large integer | Existing transport suite plus Office JSON fixture |
| Functions/custom/namespaces/history/additional_tools | Existing atomic transport/replay suites |
| Chat developer/image/forced choice | New conversion regressions |
| Chat BPS text/tools, buffered and streaming | Ten ingress fixtures and live canaries |
| Incomplete/failed/EOF/timeouts | Honest terminal states; no fabricated success or replay after output |
| JSON/schema output | Existing buffered final validation, not upstream constrained decoding |
| Search/files/audio/stateful references | Native route fixtures; upstream support still required |

Fixtures validate transport, not real workbook execution. Live probes use synthetic images and echo-only tools; they never execute submitted Office/shell source. Full CI, rollback verification and immutable-image staged/public checks are release gates. Exact observed outcomes are stored in the transaction ledger rather than asserted in advance here.

## Operational findings and remaining boundaries

The pre-change audit found repeated aggregate-image-limit failures, upstream model-access 403s, overload/rate limits and incomplete streams. Recovered errors with downstream status 200 are not user-visible failures. Raw request bodies were not retained for the older unknown-content failure, so its contents cannot be reconstructed reliably.

BPS has no located public contract promising Responses equivalence. File IDs belong to an account/project; routing cannot grant access to other owners' files. Audio, pro mode, hosted tools, WebSocket steering and async execution depend on upstream support. HTTP BPS cannot emulate an interactive WebSocket session by removing fields.

Model/detail/effort support varies. Keep the selected model and report access failures separately. Existing observable BPS effort normalization is narrower than the public API. Preserve it until upstream support is demonstrated.

Recovery stays bounded and before visible output/tool dispatch. After partial output, preserve the actual failure. Never retry indefinitely, duplicate side effects, guess Office code/tool names, substitute descriptions for unseen images or change credentials.

Future changes require a failing fixture plus a source reference or sanitized observed shape, semantic round-trip checks, concurrency/negative tests, digest-pinned staging and response-client-ID usage correlation before cutover. Retain rollback and old image-token routes during drain.
