# Integration tests — total   (latest merge: d225aa9f)

Run:        2026-06-08T22:29:02Z   (runID daa2)
Duration:   55.32s
Total:      18 test cases

## Confusion matrix

                        true (pass)   false (fail)
  +ve (through)         11            0            
  -ve (error/warning)   7             0            

## Status breakdown

              pass    fail
APPROVED      0       0      
DRAFT         18      0      

## Cases

| case | latest | best | worst | reads | cascades | duration |
|------|--------|------|-------|-------|----------|----------|
| cross-site-message-federation/stub | fail | fail | fail | — | 0 | 15.1s |
| cross-site-room-rename-federation/stub | pass | pass | fail | — | 0 | 12.0s |
| cross-site-seed-visibility/stub | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-empty-content-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-empty-content-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-bot-bypass/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-bot-bypass[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-member-blocked/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-member-blocked[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-large-room-owner-bypass/stub | pass | pass | fail | — | 0 | 108ms |
| gatekeeper-large-room-owner-bypass[c=0 p=- x=-] | pass | pass | pass | — | 0 | 108ms |
| gatekeeper-large-room-thread-reply-exempt/stub | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-large-room-thread-reply-exempt[c=0 p=- x=-] | pass | pass | pass | — | 0 | 102ms |
| gatekeeper-malformed-requestid-silent-drop/stub | pass | pass | fail | — | 0 | 10.1s |
| gatekeeper-malformed-requestid-silent-drop[c=0 p=- x=-] | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-not-subscribed-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-not-subscribed-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-cross-context-mismatch-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-quote-happy-path-embeds-snapshot/stub | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-quote-happy-path-embeds-snapshot[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-quote-nonexistent-parent-drops-message/stub | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-quote-nonexistent-parent-drops-message[c=0 p=- x=-] | pass | pass | pass | — | 0 | 10.1s |
| gatekeeper-thread-reply-missing-parent-createdat-rejected/stub | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-thread-reply-missing-parent-createdat-rejected[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.2s |
| gatekeeper-whitespace-only-content-accepted/stub | pass | pass | pass | — | 0 | 104ms |
| gatekeeper-whitespace-only-content-accepted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| history-service-get-message-by-id/stub | pass | pass | pass | — | 0 | 103ms |
| infra-sanity-rooms-pipeline-site-a/stub | pass | pass | fail | — | 0 | 205ms |
| infra-sanity-rooms-pipeline-site-b/stub | pass | pass | fail | — | 0 | 205ms |
| logs-tail-positive-captures-request-failed/stub | pass | pass | pass | — | 0 | 122ms |
| logs-tail-regression-guard-not-must-fail-when-line-present/stub | fail | fail | fail | — | 0 | 111ms |
| message-pipeline-send-and-persist/stub | pass | pass | pass | — | 0 | 211ms |
| message-pipeline-send-and-persist[c=0 p=- x=-] | pass | pass | pass | — | 0 | 211ms |
| message-worker-at-all-mention-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-at-all-mention-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| message-worker-mentions-persisted/stub | pass | pass | pass | — | 0 | 104ms |
| message-worker-mentions-persisted[c=0 p=- x=-] | pass | pass | pass | — | 0 | 104ms |
| room-create-federates-cross-site/stub | fail | fail | fail | — | 0 | 5.0s |
| room-creates-federates-to-site-b/stub | fail | fail | fail | — | 0 | 5.0s |
| single-site-room-create-baseline/stub | pass | pass | pass | — | 0 | 103ms |
| thread-first-reply-happy-path/stub | pass | pass | fail | — | 0 | 106ms |
| thread-first-reply-happy-path[c=0 p=- x=-] | pass | pass | pass | — | 0 | 106ms |
| thread-first-reply-remote-parent-federates-subscription/stub | pass | pass | fail | — | 0 | 224ms |
| thread-first-reply-remote-parent-federates-subscription[c=0 p=- x=-] | pass | pass | pass | — | 0 | 224ms |
| thread-reply-to-nonexistent-parent-creates-orphan/stub | pass | pass | pass | — | 0 | 5.1s |
| thread-reply-to-nonexistent-parent-creates-orphan[c=0 p=- x=-] | pass | pass | pass | — | 0 | 5.1s |
